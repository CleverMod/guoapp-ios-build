class LiveChannel {
  const LiveChannel({
    required this.id,
    required this.name,
    required this.group,
    this.epgId = '',
    this.epgUrl = '',
    this.catchupDays = 0,
  });

  final String id;
  final String name;
  final String group;
  final String epgId;
  final String epgUrl;
  final int catchupDays;

  factory LiveChannel.fromJson(Map<String, dynamic> json) => LiveChannel(
    id: json['id'] as String,
    name: json['name'] as String,
    group: json['group'] as String,
    epgId: json['epgId'] as String? ?? '',
    epgUrl: json['epgUrl'] as String? ?? '',
    catchupDays: json['catchupDays'] as int? ?? 0,
  );
}

class LivePlayback {
  const LivePlayback({
    required this.url,
    required this.session,
    this.headers = const {},
    this.info = const LiveStreamInfo(),
  });

  final String url;
  final String session;
  final Map<String, String> headers;
  final LiveStreamInfo info;

  factory LivePlayback.fromJson(Map<String, dynamic> json) => LivePlayback(
    url: json['url'] as String,
    session: json['session'] as String,
    headers: Map<String, String>.from(json['headers'] as Map? ?? const {}),
    info: LiveStreamInfo.fromJson(
      Map<String, dynamic>.from(json['liveInfo'] as Map? ?? const {}),
    ),
  );
}

class LiveSettings {
  const LiveSettings({
    this.deviceMode = 'all',
    this.webEnabled = true,
    this.linksPerDevice = 6,
    this.cacheMB = 200,
    this.gatewayLAN = false,
    this.gatewayPort = 8767,
    this.warning = '',
  });
  final String deviceMode;
  final bool webEnabled;
  final int linksPerDevice;
  final int cacheMB;
  final bool gatewayLAN;
  final int gatewayPort;
  final String warning;

  factory LiveSettings.fromJson(Map<String, dynamic> json) => LiveSettings(
    deviceMode: json['deviceMode'] as String? ?? 'all',
    webEnabled: json['webEnabled'] != false,
    linksPerDevice: (json['linksPerDevice'] as num?)?.toInt() ?? 6,
    cacheMB: (json['cacheMB'] as num?)?.toInt() ?? 200,
    gatewayLAN: json['gatewayLAN'] == true,
    gatewayPort: (json['gatewayPort'] as num?)?.toInt() ?? 8767,
    warning: json['warning'] as String? ?? '',
  );
  Map<String, dynamic> toJson() => {
    'deviceMode': deviceMode,
    'webEnabled': webEnabled,
    'linksPerDevice': linksPerDevice,
    'cacheMB': cacheMB,
    'gatewayLAN': gatewayLAN,
    'gatewayPort': gatewayPort,
  };
}

class LiveStreamInfo {
  const LiveStreamInfo({
    this.route = '',
    this.rate = '',
    this.rateName = '',
    this.width = 0,
    this.height = 0,
    this.bandwidth = 0,
    this.decoder = false,
  });
  final String route;
  final String rate;
  final String rateName;
  final int width;
  final int height;
  final int bandwidth;
  final bool decoder;

  factory LiveStreamInfo.fromJson(Map<String, dynamic> json) => LiveStreamInfo(
    route: json['route'] as String? ?? '',
    rate: json['rate'] as String? ?? '',
    rateName: json['rateName'] as String? ?? '',
    width: (json['width'] as num?)?.toInt() ?? 0,
    height: (json['height'] as num?)?.toInt() ?? 0,
    bandwidth: (json['bandwidth'] as num?)?.toInt() ?? 0,
    decoder: json['decoder'] == true,
  );
  String get routeLabel => switch (route) {
    'device' => '设备高码率',
    'jce' => '标准直播',
    'bk' => '备用直播',
    'web' => '网页直播',
    _ => '正在获取直播信息',
  };
  String get rateLabel => rateName.isNotEmpty
      ? rateName
      : switch (rate) {
          '36p' => '超高码率',
          '10p' => '高码率',
          _ => '',
        };
}
