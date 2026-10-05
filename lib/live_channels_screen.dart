import 'package:flutter/material.dart';

import 'app_layout.dart';
import 'core_bridge.dart';
import 'live_models.dart';
import 'live_player_screen.dart';
import 'local_store.dart';
import 'remote_widgets.dart';

class LiveChannelsScreen extends StatefulWidget {
  const LiveChannelsScreen({
    super.key,
    required this.repository,
    required this.store,
    this.embedded = false,
  });

  final AppRepository repository;
  final LocalStore store;
  final bool embedded;

  @override
  State<LiveChannelsScreen> createState() => _LiveChannelsScreenState();
}

class _LiveChannelsScreenState extends State<LiveChannelsScreen> {
  List<LiveChannel> _channels = const [];
  bool _loading = true;
  String? _error;
  String _group = '全部';
  String _query = '';
  late final int _epoch;

  @override
  void initState() {
    super.initState();
    _epoch = widget.store.profileEpoch;
    widget.store.addListener(_accessChanged);
    _load();
  }

  void _accessChanged() {
    if (mounted) setState(() {});
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final channels = await widget.repository.liveChannels();
      if (mounted) {
        setState(() {
          _channels = channels;
          _loading = false;
        });
      }
    } catch (error) {
      if (mounted) {
        setState(() {
          _error = error.toString();
          _loading = false;
        });
      }
    }
  }

  @override
  void dispose() {
    widget.store.removeListener(_accessChanged);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final allowed = !widget.store.locked && widget.store.profileEpoch == _epoch;
    final television = AppLayout.isTelevision(context);
    final channels = _channels
        .where(
          (channel) =>
              (_group == '全部' || channel.group == _group) &&
              channel.name.toLowerCase().contains(_query.trim().toLowerCase()),
        )
        .toList();
    return Scaffold(
      appBar: widget.embedded ? null : AppBar(title: const Text('电视直播 · 央视频')),
      body: SafeArea(
        top: !widget.embedded,
        child: !allowed
            ? const Center(child: Text('当前用户已变更，请返回后重新打开直播'))
            : _loading
            ? const Center(child: CircularProgressIndicator())
            : _error != null
            ? Center(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Padding(
                      padding: const EdgeInsets.all(24),
                      child: Text(_error!),
                    ),
                    FilledButton(onPressed: _load, child: const Text('重试')),
                  ],
                ),
              )
            : Column(
                children: [
                  Padding(
                    padding: const EdgeInsets.fromLTRB(16, 12, 16, 8),
                    child: TextField(
                      onChanged: (query) => setState(() => _query = query),
                      decoration: const InputDecoration(
                        prefixIcon: Icon(Icons.search),
                        hintText: '搜索频道',
                      ),
                    ),
                  ),
                  SizedBox(
                    height: 52,
                    child: ListView(
                      scrollDirection: Axis.horizontal,
                      padding: const EdgeInsets.symmetric(horizontal: 16),
                      children: [
                        for (final group in [
                          '全部',
                          '央视',
                          'CGTN',
                          '剧场',
                          '卫视',
                          '其他',
                        ])
                          Padding(
                            padding: const EdgeInsets.only(right: 8),
                            child: ChoiceChip(
                              label: Text(group),
                              selected: group == _group,
                              onSelected: (_) => setState(() => _group = group),
                            ),
                          ),
                      ],
                    ),
                  ),
                  Padding(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 16,
                      vertical: 8,
                    ),
                    child: Text(
                      '${_channels.length} 个频道配置 · 画质以实际直播为准',
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ),
                  Expanded(
                    child: channels.isEmpty
                        ? const Center(child: Text('没有匹配的频道'))
                        : ListView.builder(
                            padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
                            itemCount: channels.length,
                            itemBuilder: (context, index) {
                              final channel = channels[index];
                              return Padding(
                                padding: const EdgeInsets.only(bottom: 8),
                                child: RemoteTarget(
                                  autofocus: television && index == 0,
                                  label: channel.name,
                                  onPressed: () => Navigator.push(
                                    context,
                                    MaterialPageRoute<void>(
                                      builder: (_) => LivePlayerScreen(
                                        repository: widget.repository,
                                        store: widget.store,
                                        channels: _channels,
                                        initialChannel: channel,
                                      ),
                                    ),
                                  ),
                                  child: ListTile(
                                    leading: const Icon(Icons.live_tv_rounded),
                                    title: Text(channel.name),
                                    subtitle: Text(
                                      channel.catchupDays > 0
                                          ? '${channel.group} · ${channel.catchupDays}天回看'
                                          : channel.group,
                                    ),
                                    trailing: const Icon(
                                      Icons.play_arrow_rounded,
                                    ),
                                  ),
                                ),
                              );
                            },
                          ),
                  ),
                ],
              ),
      ),
    );
  }
}
