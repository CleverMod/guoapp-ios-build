import 'dart:async';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:media_kit/media_kit.dart';
import 'package:media_kit_video/media_kit_video.dart';

import 'app_layout.dart';
import 'app_orientation.dart';
import 'core_bridge.dart';
import 'diary_service.dart';
import 'live_models.dart';
import 'local_store.dart';
import 'luna_exo_player.dart';
import 'remote_widgets.dart';

class LivePlayerScreen extends StatefulWidget {
  const LivePlayerScreen({
    super.key,
    required this.repository,
    required this.store,
    required this.channels,
    required this.initialChannel,
  });

  final AppRepository repository;
  final LocalStore store;
  final List<LiveChannel> channels;
  final LiveChannel initialChannel;

  @override
  State<LivePlayerScreen> createState() => _LivePlayerScreenState();
}

class _LivePlayerScreenState extends State<LivePlayerScreen>
    with WidgetsBindingObserver {
  late final Player _player;
  VideoController? _video;
  late LiveChannel _channel;
  late final int _epoch;
  final _subscriptions = <StreamSubscription<dynamic>>[];
  AppOrientationController? _orientation;
  LivePlayback? _playback;
  Future<void> _operations = Future<void>.value();
  Timer? _healthTimer;
  Timer? _retryTimer;
  DateTime _lastProgress = DateTime.now();
  Duration _lastPosition = Duration.zero;
  int _generation = 0;
  int _retries = 0;
  bool _closed = false;
  bool _loading = true;
  bool _acceptErrors = false;
  bool _foreground = true;
  bool _playIntent = true;
  bool _fullscreen = false;
  bool _channelPicker = false;
  double _volume = 100;
  bool _volumeReady = !AppDevice.supportsMediaVolume;
  bool _settingVolume = false;
  double? _pendingVolume;
  String? _error;

  bool get _allowed =>
      !widget.store.locked && widget.store.profileEpoch == _epoch;

  @override
  void initState() {
    super.initState();
    _epoch = widget.store.profileEpoch;
    _channel = widget.initialChannel;
    _player = Platform.isAndroid
        ? LunaExoPlayer()
        : Player(
            configuration: const PlayerConfiguration(
              bufferSize: 16 * 1024 * 1024,
              logLevel: MPVLogLevel.warn,
            ),
          );
    if (!Platform.isAndroid) {
      _video = VideoController(
        _player,
        configuration: VideoControllerConfiguration(
          enableHardwareAcceleration: !Platform.isIOS,
        ),
      );
    }
    WidgetsBinding.instance.addObserver(this);
    widget.store.addListener(_accessChanged);
    _subscriptions.add(
      _player.stream.error.listen((error) {
        if (_acceptErrors && !_closed && error.isNotEmpty) {
          _recover('直播播放出错，请重新取流');
        }
      }),
    );
    _subscriptions.add(
      _player.stream.position.listen((position) {
        if (position != _lastPosition) {
          _lastPosition = position;
          _lastProgress = DateTime.now();
        }
      }),
    );
    _subscriptions.add(
      _player.stream.completed.listen((completed) {
        if (completed && _acceptErrors && !_closed) _recover('直播流已中断');
      }),
    );
    _subscriptions.add(
      _player.stream.playing.listen((_) {
        if (mounted) setState(() {});
      }),
    );
    _subscriptions.add(
      _player.stream.buffering.listen((_) {
        if (mounted) setState(() {});
      }),
    );
    if (AppDevice.supportsMediaVolume) {
      _subscriptions.add(
        AppDevice.mediaVolumeChanges.listen(
          _receiveVolume,
          onError: (Object _) => DiaryService.add('[Live] 系统媒体音量监听失败'),
        ),
      );
      unawaited(
        AppDevice.getMediaVolume()
            .then(_receiveVolume)
            .catchError((Object _) => DiaryService.add('[Live] 系统媒体音量读取失败')),
      );
    }
    _healthTimer = Timer.periodic(const Duration(seconds: 2), (_) {
      if (!_loading &&
          _error == null &&
          _playIntent &&
          _foreground &&
          _allowed &&
          DateTime.now().difference(_lastProgress) >
              const Duration(seconds: 20)) {
        _recover('直播长时间未更新，请重新取流');
      }
    });
    _load(_channel);
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _orientation = AppOrientationScope.maybeOf(context);
  }

  void _accessChanged() {
    if (!_allowed && !_closed) {
      _generation++;
      _acceptErrors = false;
      _playIntent = false;
      _retryTimer?.cancel();
      unawaited(_player.pause());
      final playback = _playback;
      _playback = null;
      if (playback != null) unawaited(_release(playback));
      if (mounted) {
        setState(() {
          _loading = false;
          _error = '当前用户已变更，请退出直播';
        });
      }
    }
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (_closed) return;
    _foreground = state == AppLifecycleState.resumed;
    if (!_foreground) {
      _retryTimer?.cancel();
      unawaited(_player.pause());
    } else if (_playIntent && _allowed) {
      _load(_channel);
    }
  }

  Future<void> _release(LivePlayback playback) async {
    try {
      await widget.repository.releaseLive(playback.session);
    } catch (_) {}
  }

  void _load(LiveChannel channel, {bool automatic = false}) {
    if (_closed || !_allowed) return;
    final ticket = ++_generation;
    _retryTimer?.cancel();
    if (!automatic) _retries = 0;
    _acceptErrors = false;
    _playIntent = true;
    setState(() {
      _channel = channel;
      _loading = true;
      _error = null;
    });
    _operations = _operations.catchError((Object _) {}).then((_) async {
      if (_closed || ticket != _generation || !_allowed) return;
      final previous = _playback;
      _playback = null;
      LivePlayback? playback;
      try {
        try {
          await _player.stop();
        } finally {
          if (previous != null) await _release(previous);
        }
        playback = await widget.repository.openLive(channel.id);
        if (_closed || ticket != _generation || !_allowed) {
          await _release(playback);
          return;
        }
        _playback = playback;
        final platform = _player.platform;
        if (platform is NativePlayer) {
          if (Platform.isIOS) await platform.setProperty('cache-on-disk', 'no');
          await platform.setProperty('network-timeout', '20');
        }
        DiaryService.add('[Live] 打开频道 ${channel.id}，第 ${_retries + 1} 次取流');
        await _player.open(
          Media(playback.url, httpHeaders: playback.headers),
          play: _foreground && _playIntent,
        );
        if (_closed || ticket != _generation || !_allowed) {
          await _player.stop();
          if (identical(_playback, playback)) {
            _playback = null;
            await _release(playback);
          }
          return;
        }
        await _player.setVolume(AppDevice.supportsMediaVolume ? 100 : _volume);
        if (!_foreground || !_playIntent) await _player.pause();
        _lastPosition = _player.state.position;
        _lastProgress = DateTime.now();
        _acceptErrors = true;
        setState(() {
          _loading = false;
        });
      } catch (error) {
        if (playback != null) {
          await _release(playback);
          if (identical(_playback, playback)) _playback = null;
        }
        if (!_closed && ticket == _generation && _allowed) {
          final message = error is AppFailure ? error.message : '直播初始化失败，请重新取流';
          setState(() {
            _loading = false;
            _error = message;
          });
          DiaryService.add('[Live] 频道 ${channel.id} 取流失败');
          _recover(message);
        }
      }
    });
  }

  void _recover(String message) {
    if (_closed ||
        !_foreground ||
        !_playIntent ||
        !_allowed ||
        _retryTimer?.isActive == true) {
      return;
    }
    _acceptErrors = false;
    setState(() {
      _error = message;
    });
    if (_retries >= 2) return;
    _retries++;
    final ticket = _generation;
    DiaryService.add('[Live] 频道 ${_channel.id} 将重新取流（$_retries/2）');
    _retryTimer = Timer(Duration(seconds: _retries * 2), () {
      if (!_closed && ticket == _generation && _allowed) {
        _load(_channel, automatic: true);
      }
    });
  }

  void _changeChannel(int step) {
    final index = widget.channels.indexWhere(
      (channel) => channel.id == _channel.id,
    );
    _load(
      widget.channels[(index + step + widget.channels.length) %
          widget.channels.length],
    );
  }

  Future<void> _togglePlayback() async {
    if (_loading || !_allowed) return;
    if (_playIntent) {
      _playIntent = false;
      _retryTimer?.cancel();
      await _player.pause();
      if (mounted) setState(() {});
    } else {
      _load(_channel);
    }
  }

  void _receiveVolume(double value) {
    if (!_closed && mounted) {
      setState(() {
        _volume = value.clamp(0.0, 1.0) * 100;
        _volumeReady = true;
      });
    }
  }

  void _setVolume(double value) {
    if (_closed || !_allowed || !_volumeReady) return;
    if (!AppDevice.supportsMediaVolume) {
      setState(() => _volume = value);
      unawaited(_player.setVolume(value));
      return;
    }
    _pendingVolume = value;
    if (!_settingVolume) unawaited(_flushVolume());
  }

  Future<void> _flushVolume() async {
    _settingVolume = true;
    try {
      while (!_closed && _allowed && _pendingVolume != null) {
        final value = _pendingVolume!;
        _pendingVolume = null;
        await AppDevice.setMediaVolume(value / 100);
      }
    } catch (_) {
      DiaryService.add('[Live] 系统媒体音量调整失败');
      if (!_closed && mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('系统媒体音量调整失败')));
      }
    } finally {
      _pendingVolume = null;
      _settingVolume = false;
    }
  }

  Future<void> _setFullscreen(bool value) async {
    if (_closed) return;
    setState(() => _fullscreen = value);
    await _orientation?.setPlayback(
      this,
      fullscreen: value,
      aspectRatio: 16 / 9,
    );
  }

  Future<void> _chooseChannel() async {
    if (_channelPicker || !_allowed) return;
    _channelPicker = true;
    final selectedIndex = widget.channels.indexWhere(
      (channel) => channel.id == _channel.id,
    );
    final scroll = ScrollController(
      initialScrollOffset:
          selectedIndex.clamp(0, widget.channels.length - 1) * 88.0,
    );
    try {
      final selected = await showDialog<LiveChannel>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('选择频道'),
          content: SizedBox(
            width: 480,
            height: 420,
            child: ListView.builder(
              controller: scroll,
              itemExtent: 88,
              itemCount: widget.channels.length,
              itemBuilder: (context, index) {
                final channel = widget.channels[index];
                return RemoteTarget(
                  selected: channel.id == _channel.id,
                  autofocus: channel.id == _channel.id,
                  onPressed: () => Navigator.pop(context, channel),
                  child: ListTile(
                    title: Text(channel.name),
                    subtitle: Text(channel.group),
                  ),
                );
              },
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context),
              child: const Text('关闭'),
            ),
          ],
        ),
      );
      if (selected != null && !_closed) _load(selected);
    } finally {
      scroll.dispose();
      _channelPicker = false;
    }
  }

  @override
  void dispose() {
    _closed = true;
    _generation++;
    _retryTimer?.cancel();
    _healthTimer?.cancel();
    widget.store.removeListener(_accessChanged);
    WidgetsBinding.instance.removeObserver(this);
    for (final subscription in _subscriptions) {
      unawaited(subscription.cancel());
    }
    unawaited(_orientation?.releasePlayback(this) ?? Future<void>.value());
    unawaited(_player.pause());
    unawaited(
      _operations.catchError((Object _) {}).then((_) async {
        final playback = _playback;
        try {
          await _player.dispose();
        } finally {
          if (playback != null) await _release(playback);
        }
      }),
    );
    super.dispose();
  }

  Widget _videoPane() {
    final overlay = Stack(
      fit: StackFit.expand,
      children: [
        if (_loading) const Center(child: CircularProgressIndicator()),
        if (_error != null)
          Center(
            child: Container(
              margin: const EdgeInsets.all(20),
              padding: const EdgeInsets.all(20),
              decoration: BoxDecoration(
                color: Colors.black.withValues(alpha: .8),
                borderRadius: BorderRadius.circular(16),
              ),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(_error!, textAlign: TextAlign.center),
                  const SizedBox(height: 12),
                  FilledButton(
                    onPressed: _allowed ? () => _load(_channel) : null,
                    child: const Text('重新取流'),
                  ),
                ],
              ),
            ),
          ),
        Align(
          alignment: Alignment.bottomCenter,
          child: Container(
            color: Colors.black.withValues(alpha: .72),
            padding: const EdgeInsets.symmetric(horizontal: 8),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  children: [
                    IconButton(
                      tooltip: '上一个频道',
                      onPressed: _allowed ? () => _changeChannel(-1) : null,
                      icon: const Icon(Icons.skip_previous),
                    ),
                    IconButton(
                      autofocus: AppLayout.isTelevision(context),
                      tooltip: _playIntent ? '暂停' : '播放直播',
                      onPressed: _allowed && !_loading ? _togglePlayback : null,
                      icon: Icon(_playIntent ? Icons.pause : Icons.play_arrow),
                    ),
                    IconButton(
                      tooltip: '下一个频道',
                      onPressed: _allowed ? () => _changeChannel(1) : null,
                      icon: const Icon(Icons.skip_next),
                    ),
                    Expanded(
                      child: Text(
                        '直播 · ${_channel.name}',
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                    IconButton(
                      tooltip: _fullscreen ? '退出全屏' : '全屏',
                      onPressed: () => _setFullscreen(!_fullscreen),
                      icon: Icon(
                        _fullscreen ? Icons.fullscreen_exit : Icons.fullscreen,
                      ),
                    ),
                  ],
                ),
                Row(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    TextButton.icon(
                      onPressed: _allowed ? _chooseChannel : null,
                      icon: const Icon(Icons.list),
                      label: const Text('频道'),
                    ),
                    TextButton.icon(
                      onPressed: _allowed ? () => _load(_channel) : null,
                      icon: const Icon(Icons.refresh),
                      label: const Text('回到直播'),
                    ),
                    PopupMenuButton<double>(
                      tooltip: '音量',
                      enabled: _volumeReady && _allowed,
                      icon: const Icon(Icons.volume_up),
                      initialValue: _volume,
                      onSelected: _setVolume,
                      itemBuilder: (_) => [
                        for (final value in [0.0, 25.0, 50.0, 75.0, 100.0])
                          PopupMenuItem(
                            value: value,
                            child: Text('音量 ${value.round()}%'),
                          ),
                      ],
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ],
    );
    return Theme(
      data: ThemeData.dark(useMaterial3: true),
      child: ColoredBox(
        color: Colors.black,
        child: _player is LunaExoPlayer
            ? LunaExoVideoView(player: _player, controls: (_) => overlay)
            : Video(
                controller: _video!,
                fit: BoxFit.contain,
                controls: (_) => overlay,
              ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final television = AppLayout.isTelevision(context);
    return PopScope(
      canPop: !_fullscreen || television,
      onPopInvokedWithResult: (didPop, _) {
        if (!didPop && _fullscreen) _setFullscreen(false);
      },
      child: CallbackShortcuts(
        bindings: {
          const SingleActivator(LogicalKeyboardKey.pageUp): () =>
              _changeChannel(-1),
          const SingleActivator(LogicalKeyboardKey.pageDown): () =>
              _changeChannel(1),
          const SingleActivator(LogicalKeyboardKey.space): () =>
              unawaited(_togglePlayback()),
          const SingleActivator(LogicalKeyboardKey.escape): () {
            if (_fullscreen) {
              _setFullscreen(false);
            } else {
              Navigator.maybePop(context);
            }
          },
        },
        child: Focus(
          autofocus: !television,
          child: Scaffold(
            appBar: _fullscreen
                ? null
                : AppBar(
                    title: Text(_channel.name),
                    actions: [
                      IconButton(
                        tooltip: '播放日记',
                        onPressed: () => DiaryService.showDiaryDialog(context),
                        icon: const Icon(Icons.receipt_long),
                      ),
                    ],
                  ),
            body: SafeArea(
              child: _fullscreen || television
                  ? _videoPane()
                  : Column(
                      children: [
                        Expanded(
                          child: Center(
                            child: AspectRatio(
                              aspectRatio: 16 / 9,
                              child: _videoPane(),
                            ),
                          ),
                        ),
                        Padding(
                          padding: const EdgeInsets.all(16),
                          child: Column(
                            children: [
                              Text('${_channel.group} · 直播画质以实际输出为准'),
                              Row(
                                children: [
                                  const Icon(Icons.volume_up),
                                  Expanded(
                                    child: Slider(
                                      value: _volume,
                                      min: 0,
                                      max: 100,
                                      onChanged: _volumeReady && _allowed
                                          ? _setVolume
                                          : null,
                                    ),
                                  ),
                                ],
                              ),
                              const Text(
                                '暂停后继续将回到直播。支持 PageUp / PageDown 换台、空格播放 / 暂停。',
                                textAlign: TextAlign.center,
                              ),
                            ],
                          ),
                        ),
                      ],
                    ),
            ),
          ),
        ),
      ),
    );
  }
}
