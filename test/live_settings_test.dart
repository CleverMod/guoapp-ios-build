import 'package:duanju_app/live_models.dart';
import 'package:duanju_app/live_settings_screen.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'fixtures.dart';

class LiveSettingsFixtureRepository extends FixtureRepository {
  LiveSettings current = const LiveSettings();
  int saves = 0, starts = 0;

  @override
  Future<LiveSettings> liveSettings() async => current;
  @override
  Future<LiveSettings> saveLiveSettings(LiveSettings settings) async {
    current = settings;
    saves++;
    return current;
  }

  @override
  Future<Map<String, dynamic>> liveDiagnostics() async => {
    'active': 1,
    'maximum': 15,
    'gatewayActive': starts > 0,
    'gateway': starts > 0
        ? ['http://127.0.0.1:8767/tv/fixture/all.m3u']
        : <String>[],
    'devices': [
      {
        'slot': 0,
        'enabled': true,
        'ready': true,
        'standby': false,
        'brand': 'Sony',
        'model': 'fixture-8K',
        'linked': 3,
        'heartbeatCount': 2,
        'heartbeatAge': 1,
        'sessionRemaining': 7000,
      },
    ],
    'channels': [
      {
        'id': 'cctv4k',
        'name': 'CCTV-4K',
        'active': true,
        'info': {
          'route': 'device',
          'width': 3840,
          'height': 2160,
          'decoder': true,
        },
        'refreshAge': 1,
        'cooldown': 0,
      },
    ],
    'cache': {
      'bytes': 4 * 1024 * 1024,
      'limitMB': current.cacheMB,
      'entries': 2,
      'hits': 3,
      'misses': 1,
      'hitRate': 75.0,
    },
    'events': <Object>[],
  };

  @override
  Future<Map<String, dynamic>> liveGateway(String command) async {
    starts = command == 'start' ? starts + 1 : 0;
    return liveDiagnostics();
  }
}

void main() {
  test('live settings preserve zero quota/cache and LAN scope', () {
    const settings = LiveSettings(
      deviceMode: '4k',
      linksPerDevice: 0,
      cacheMB: 0,
      gatewayLAN: true,
      gatewayPort: 8877,
    );
    final decoded = LiveSettings.fromJson(settings.toJson());
    expect(decoded.deviceMode, '4k');
    expect(decoded.linksPerDevice, 0);
    expect(decoded.cacheMB, 0);
    expect(decoded.gatewayLAN, isTrue);
    expect(decoded.gatewayPort, 8877);
  });

  test('unknown live resolution is not labeled 1080p', () {
    final playback = LivePlayback.fromJson({
      'url': 'http://media.test/live.m3u8',
      'session': 'fixture',
      'headers': <String, String>{},
    });
    expect(playback.info.height, 0);
    final info = LiveStreamInfo.fromJson({
      'route': 'device',
      'rate': '36p',
      'width': 7680,
      'height': 4320,
      'decoder': true,
    });
    expect(info.height, 4320);
    expect(info.decoder, isTrue);
    expect(info.rateLabel, '超高码率');
  });

  testWidgets(
    'live settings and diagnostics fit a phone without opening subscription automatically',
    (tester) async {
      SharedPreferences.setMockInitialValues({});
      final store = testStore(await SharedPreferences.getInstance());
      final repository = LiveSettingsFixtureRepository();
      tester.view.physicalSize = const Size(390, 844);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      await tester.pumpWidget(
        MaterialApp(
          home: LiveSettingsScreen(repository: repository, store: store),
        ),
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 100));
      expect(find.text('直播设置与诊断'), findsOneWidget);
      expect(find.text('直播运行'), findsOneWidget);
      expect(repository.starts, 0);
      expect(tester.takeException(), isNull);
      await tester.ensureVisible(find.text('保存直播设置'));
      await tester.tap(find.text('保存直播设置'));
      await tester.pump(const Duration(milliseconds: 100));
      expect(repository.saves, 1);
      await tester.ensureVisible(find.text('运行诊断'));
      await tester.pump();
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox.shrink());
      store.dispose();
    },
  );
}
