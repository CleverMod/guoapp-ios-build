import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'core_bridge.dart';
import 'live_models.dart';
import 'local_store.dart';

class LiveSettingsScreen extends StatefulWidget {
  const LiveSettingsScreen({
    super.key,
    required this.repository,
    required this.store,
  });
  final AppRepository repository;
  final LocalStore store;
  @override
  State<LiveSettingsScreen> createState() => _LiveSettingsScreenState();
}

class _LiveSettingsScreenState extends State<LiveSettingsScreen> {
  late final int _epoch = widget.store.profileEpoch;
  final _port = TextEditingController();
  LiveSettings? _settings;
  Map<String, dynamic> _diagnostics = const {};
  String _mode = 'all';
  int _links = 6, _cache = 200;
  bool _lan = false, _busy = false, _allChannels = false;
  String? _error;
  Timer? _timer;
  bool _refreshing = false;

  bool get _allowed =>
      !widget.store.locked && widget.store.profileEpoch == _epoch;
  bool get _editable => _allowed && widget.store.profile.admin;
  bool get _gatewayActive => _diagnostics['gatewayActive'] == true;

  @override
  void initState() {
    super.initState();
    widget.store.addListener(_accessChanged);
    _load();
    _timer = Timer.periodic(const Duration(seconds: 5), (_) => _refresh());
  }

  void _accessChanged() {
    if (mounted) setState(() {});
  }

  Future<void> _load() async {
    try {
      final values = await Future.wait<Object>([
        widget.repository.liveSettings(),
        widget.repository.liveDiagnostics(),
      ]);
      if (!mounted || !_allowed) return;
      final settings = values[0] as LiveSettings;
      setState(() {
        _settings = settings;
        _mode = settings.deviceMode;
        _links = settings.linksPerDevice;
        _cache = settings.cacheMB;
        _lan = settings.gatewayLAN;
        _port.text = '${settings.gatewayPort}';
        _diagnostics = values[1] as Map<String, dynamic>;
        _error = settings.warning.isEmpty ? null : settings.warning;
      });
    } catch (error) {
      if (mounted) setState(() => _error = error.toString());
    }
  }

  Future<void> _refresh() async {
    if (!_allowed || _refreshing || _busy) return;
    _refreshing = true;
    try {
      final diagnostics = await widget.repository.liveDiagnostics();
      if (mounted && _allowed) setState(() => _diagnostics = diagnostics);
    } catch (_) {
    } finally {
      _refreshing = false;
    }
  }

  Future<void> _save() async {
    if (!_editable || _busy) return;
    final port = int.tryParse(_port.text.trim());
    if (port == null || port < 1024 || port > 65535) {
      setState(() => _error = '端口应为 1024 至 65535');
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final saved = await widget.repository.saveLiveSettings(
        LiveSettings(
          deviceMode: _mode,
          linksPerDevice: _links,
          cacheMB: _cache,
          gatewayLAN: _lan,
          gatewayPort: port,
        ),
      );
      if (!mounted || !_allowed) return;
      setState(() => _settings = saved);
      ScaffoldMessenger.of(
        context,
      ).showSnackBar(const SnackBar(content: Text('直播设置已保存')));
    } catch (error) {
      if (mounted) setState(() => _error = error.toString());
    } finally {
      if (mounted) setState(() => _busy = false);
      await _refresh();
    }
  }

  Future<void> _command(String command) async {
    if (!_allowed || _busy || command != 'clear' && !_editable) return;
    if (command == 'start') {
      await _save();
      if (!mounted || !_allowed || _error != null) return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final diagnostics = command == 'clear'
          ? await widget.repository.clearLiveCache()
          : await widget.repository.liveGateway(command);
      if (mounted && _allowed) setState(() => _diagnostics = diagnostics);
    } catch (error) {
      if (mounted) setState(() => _error = error.toString());
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _copy(String value, String label) async {
    if (!_allowed) return;
    await Clipboard.setData(ClipboardData(text: value));
    if (mounted && _allowed) {
      ScaffoldMessenger.of(
        context,
      ).showSnackBar(SnackBar(content: Text('$label已复制')));
    }
  }

  Future<void> _copyDiagnostics() async {
    final safe = Map<String, dynamic>.from(_diagnostics);
    safe.remove('gateway');
    await _copy(const JsonEncoder.withIndent('  ').convert(safe), '诊断信息');
  }

  String _age(Object? value) {
    final age = (value as num?)?.toInt() ?? -1;
    return age < 0 ? '尚无记录' : '$age 秒前';
  }

  @override
  void dispose() {
    _timer?.cancel();
    widget.store.removeListener(_accessChanged);
    _port.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final cache = Map<String, dynamic>.from(
      _diagnostics['cache'] as Map? ?? const {},
    );
    final devices = (_diagnostics['devices'] as List? ?? const []).cast<Map>();
    final allChannels = (_diagnostics['channels'] as List? ?? const [])
        .cast<Map>();
    final channels = allChannels.where(
      (row) =>
          _allChannels ||
          row['active'] == true ||
          (row['cooldown'] as num? ?? 0) > 0,
    );
    final urls = (_diagnostics['gateway'] as List? ?? const []).cast<String>();
    final events = (_diagnostics['events'] as List? ?? const []).cast<Map>();
    final cacheChoices = {0, 50, 100, 200, 300, 512, _cache}.toList()..sort();
    return Scaffold(
      appBar: AppBar(
        title: const Text('直播设置与诊断'),
        actions: [
          IconButton(
            tooltip: '刷新诊断',
            onPressed: _allowed && !_busy ? _refresh : null,
            icon: const Icon(Icons.refresh),
          ),
          IconButton(
            tooltip: '复制诊断信息',
            onPressed: _allowed ? _copyDiagnostics : null,
            icon: const Icon(Icons.copy_all),
          ),
        ],
      ),
      body: !_allowed
          ? const Center(child: Text('当前用户已变更，请重新打开'))
          : _settings == null && _error == null
          ? const Center(child: CircularProgressIndicator())
          : Center(
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 760),
                child: ListView(
                  padding: const EdgeInsets.all(16),
                  children: [
                    if (_error != null)
                      Padding(
                        padding: const EdgeInsets.only(bottom: 12),
                        child: Text(
                          _error!,
                          style: TextStyle(
                            color: Theme.of(context).colorScheme.error,
                          ),
                        ),
                      ),
                    if (_settings == null)
                      FilledButton(onPressed: _load, child: const Text('重新读取')),
                    if (_settings != null) ...[
                      Text(
                        '直播运行',
                        style: Theme.of(context).textTheme.titleLarge,
                      ),
                      const SizedBox(height: 12),
                      DropdownButtonFormField<String>(
                        initialValue: _mode,
                        decoration: const InputDecoration(labelText: '取流模式'),
                        items: const [
                          DropdownMenuItem(
                            value: 'all',
                            child: Text('全部设备高码率频道优先'),
                          ),
                          DropdownMenuItem(
                            value: '4k',
                            child: Text('仅 4K/8K 优先使用设备线路'),
                          ),
                          DropdownMenuItem(
                            value: 'off',
                            child: Text('标准直播与备用线路'),
                          ),
                        ],
                        onChanged: _editable && !_busy
                            ? (value) => setState(() => _mode = value!)
                            : null,
                      ),
                      const SizedBox(height: 12),
                      DropdownButtonFormField<int>(
                        initialValue: _links,
                        decoration: const InputDecoration(
                          labelText: '每设备会话的频道配额',
                        ),
                        items: [
                          for (var value = 0; value <= 26; value++)
                            DropdownMenuItem(
                              value: value,
                              child: Text(value == 0 ? '关闭主动配额轮换' : '$value 路'),
                            ),
                        ],
                        onChanged: _editable && !_busy
                            ? (value) => setState(() => _links = value!)
                            : null,
                      ),
                      const SizedBox(height: 12),
                      DropdownButtonFormField<int>(
                        initialValue: _cache,
                        decoration: const InputDecoration(
                          labelText: '直播分片内存缓存',
                        ),
                        items: [
                          for (final value in cacheChoices)
                            DropdownMenuItem(
                              value: value,
                              child: Text(value == 0 ? '关闭缓存' : '$value MB'),
                            ),
                        ],
                        onChanged: _editable && !_busy
                            ? (value) => setState(() => _cache = value!)
                            : null,
                      ),
                      const Padding(
                        padding: EdgeInsets.symmetric(vertical: 12),
                        child: Text(
                          '配额统计同一设备会话取过的不同频道。缓存只保存在内存；分片保留最多 3 分钟，超过容量会淘汰旧数据。设置在后续取流刷新时应用。',
                        ),
                      ),
                      FilledButton(
                        onPressed: _editable && !_busy ? _save : null,
                        child: const Text('保存直播设置'),
                      ),
                      const Divider(height: 32),
                      Text(
                        '播放器订阅',
                        style: Theme.of(context).textTheme.titleLarge,
                      ),
                      SwitchListTile(
                        contentPadding: EdgeInsets.zero,
                        value: _lan,
                        title: const Text('允许局域网设备访问'),
                        subtitle: const Text('默认仅本机。开启后，同一局域网播放器可使用复制的订阅链接。'),
                        onChanged: _editable && !_busy && !_gatewayActive
                            ? (value) => setState(() => _lan = value)
                            : null,
                      ),
                      TextField(
                        controller: _port,
                        enabled: _editable && !_busy && !_gatewayActive,
                        keyboardType: TextInputType.number,
                        decoration: const InputDecoration(labelText: '订阅端口'),
                      ),
                      const SizedBox(height: 12),
                      OutlinedButton.icon(
                        onPressed: _editable && !_busy
                            ? () => _command(_gatewayActive ? 'stop' : 'start')
                            : null,
                        icon: Icon(
                          _gatewayActive
                              ? Icons.stop_circle_outlined
                              : Icons.play_circle_outline,
                        ),
                        label: Text(_gatewayActive ? '关闭订阅服务' : '开启订阅服务'),
                      ),
                      const Padding(
                        padding: EdgeInsets.symmetric(vertical: 8),
                        child: Text(
                          '在外部播放器导入 M3U 链接，可使用频道分组、EPG 与回看。订阅链接只在服务开启期间有效，锁定或切换用户会停止服务。',
                        ),
                      ),
                      for (final address in urls)
                        ListTile(
                          contentPadding: EdgeInsets.zero,
                          title: SelectableText(address),
                          trailing: IconButton(
                            tooltip: '复制订阅地址',
                            onPressed: () => _copy(address, '订阅地址'),
                            icon: const Icon(Icons.copy),
                          ),
                        ),
                    ],
                    const Divider(height: 32),
                    Text('运行诊断', style: Theme.of(context).textTheme.titleLarge),
                    ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: Text(
                        '播放会话 ${_diagnostics['active'] ?? 0} / ${_diagnostics['maximum'] ?? 15}',
                      ),
                      subtitle: Text(
                        '热备状态：${_diagnostics['refilling'] == true ? '准备中' : '待命'}',
                      ),
                    ),
                    for (final device in devices)
                      ListTile(
                        contentPadding: EdgeInsets.zero,
                        title: Text(
                          '${device['standby'] == true ? '热备设备' : '设备 ${device['slot']}'} · ${device['enabled'] == false
                              ? '未启用'
                              : device['ready'] == true
                              ? '就绪'
                              : '准备中'}',
                        ),
                        subtitle: Text(
                          '${device['brand'] ?? ''} ${device['model'] ?? ''}\n已取 ${device['linked'] ?? 0} 路 · 心跳 ${device['heartbeatCount'] ?? 0} 次 · ${_age(device['heartbeatAge'])}\n会话剩余 ${device['sessionRemaining'] ?? 0} 秒${(device['heartbeatError'] as String? ?? '').isEmpty ? '' : '\n${device['heartbeatError']}'}',
                        ),
                      ),
                    ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: Text(
                        '分片缓存 ${(cache['bytes'] as num? ?? 0) / (1024 * 1024) < 0.1 ? '0' : ((cache['bytes'] as num? ?? 0) / (1024 * 1024)).toStringAsFixed(1)} / ${cache['limitMB'] ?? 0} MB',
                      ),
                      subtitle: Text(
                        '${cache['entries'] ?? 0} 条 · 命中 ${((cache['hitRate'] as num?)?.toDouble() ?? 0).toStringAsFixed(1)}% · 命中/未中 ${cache['hits'] ?? 0}/${cache['misses'] ?? 0}',
                      ),
                      trailing: IconButton(
                        tooltip: '清理直播缓存',
                        onPressed: !_busy ? () => _command('clear') : null,
                        icon: const Icon(Icons.cleaning_services_outlined),
                      ),
                    ),
                    SwitchListTile(
                      contentPadding: EdgeInsets.zero,
                      value: _allChannels,
                      title: const Text('显示全部频道状态'),
                      onChanged: (value) =>
                          setState(() => _allChannels = value),
                    ),
                    for (final channel in channels) _channelTile(channel),
                    ExpansionTile(
                      tilePadding: EdgeInsets.zero,
                      title: const Text('最近运行事件'),
                      children: [
                        for (final event in events.reversed)
                          ListTile(
                            contentPadding: EdgeInsets.zero,
                            title: Text(event['message'] as String? ?? ''),
                            subtitle: Text(
                              '${event['time'] ?? ''} ${event['channel'] ?? ''}',
                            ),
                          ),
                        if (events.isEmpty)
                          const ListTile(title: Text('暂无运行事件')),
                      ],
                    ),
                  ],
                ),
              ),
            ),
    );
  }

  Widget _channelTile(Map channel) {
    final info = LiveStreamInfo.fromJson(
      Map<String, dynamic>.from(channel['info'] as Map? ?? const {}),
    );
    final size = info.width > 0 && info.height > 0
        ? ' · ${info.width}×${info.height}${info.decoder ? '（解码器）' : '（源标注）'}'
        : '';
    return ListTile(
      contentPadding: EdgeInsets.zero,
      title: Text(
        '${channel['name']} · ${channel['active'] == true ? info.routeLabel : '未播放'}$size',
      ),
      subtitle: Text(
        '刷新：${_age(channel['refreshAge'])} · 冷却 ${channel['cooldown'] ?? 0} 秒${(channel['error'] as String? ?? '').isEmpty ? '' : '\n${channel['error']}'}',
      ),
    );
  }
}
