class LiveChannel {
  const LiveChannel({
    required this.id,
    required this.name,
    required this.group,
  });

  final String id;
  final String name;
  final String group;

  factory LiveChannel.fromJson(Map<String, dynamic> json) => LiveChannel(
    id: json['id'] as String,
    name: json['name'] as String,
    group: json['group'] as String,
  );
}

class LivePlayback {
  const LivePlayback({
    required this.url,
    required this.session,
    this.headers = const {},
  });

  final String url;
  final String session;
  final Map<String, String> headers;

  factory LivePlayback.fromJson(Map<String, dynamic> json) => LivePlayback(
    url: json['url'] as String,
    session: json['session'] as String,
    headers: Map<String, String>.from(json['headers'] as Map? ?? const {}),
  );
}
