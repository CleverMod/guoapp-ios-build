import 'dart:convert';

import 'package:duanju_app/app_build.dart';
import 'package:duanju_app/local_profiles.dart';
import 'package:duanju_app/local_snapshot.dart';
import 'package:duanju_app/models.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'fixtures.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final sourceIds = SourceSite.values.map((site) => site.id).toList();
  const retiredKeys = {
    'sourceGateEnabled',
    'sourceGateOff',
    'sourceGateSalt',
    'sourceGateHash',
  };

  test('new installations show every compiled source', () async {
    SharedPreferences.setMockInitialValues({});
    final store = testStore(await SharedPreferences.getInstance());
    addTearDown(store.dispose);

    expect(store.configurationError, isNull);
    expect(store.sources.map((site) => site.id), sourceIds);
    expect(store.sources, hasLength(allSourcesEnabled ? 13 : 1));
    for (final source in sourceIds) {
      expect(store.allowsSource(source), isTrue);
    }
  });

  test('legacy preference locks cannot hide sources after upgrade', () async {
    SharedPreferences.setMockInitialValues({
      'sourceGateEnabled': true,
      'sourceGateOff': false,
      'sourceGateSalt': '0' * 32,
      'sourceGateHash': '1' * 64,
      'source': 'yeguo',
    });
    final store = testStore(await SharedPreferences.getInstance());
    addTearDown(store.dispose);

    expect(store.configurationError, isNull);
    expect(store.sources.map((site) => site.id), sourceIds);
    expect(store.source, allSourcesEnabled ? 'yeguo' : 'hongguo');
  });

  test('damaged retired snapshot fields are ignored and not saved', () async {
    final profiles = jsonEncode([
      const LocalProfile(id: 'default', name: '管理员', admin: true).toJson(),
    ]);
    final favorites = jsonEncode([FixtureRepository.free.toJson()]);
    SharedPreferences.setMockInitialValues({
      LocalSnapshot.storageKey: jsonEncode({
        'version': 1,
        'values': {
          'profiles': profiles,
          'favorites': favorites,
          'sourceGateEnabled': 'damaged',
          'sourceGateOff': 42,
          'sourceGateSalt': null,
          'sourceGateHash': ['damaged'],
        },
      }),
    });
    final preferences = await SharedPreferences.getInstance();
    final store = testStore(preferences);
    addTearDown(store.dispose);

    expect(store.configurationError, isNull);
    expect(store.sources.map((site) => site.id), sourceIds);
    expect(store.favorites.map((drama) => drama.id), [
      FixtureRepository.free.id,
    ]);
    await store.setSource('hongguo');
    final saved = jsonDecode(preferences.getString(LocalSnapshot.storageKey)!);
    final values = saved['values'] as Map;
    expect(values.keys.any(retiredKeys.contains), isFalse);
    expect(values['favorites'], favorites);

    final reloaded = testStore(preferences);
    addTearDown(reloaded.dispose);
    expect(reloaded.configurationError, isNull);
    expect(reloaded.sources.map((site) => site.id), sourceIds);
    expect(reloaded.favorites.map((drama) => drama.id), [
      FixtureRepository.free.id,
    ]);
  });

  test(
    'unknown active snapshot fields still block invalid configuration',
    () async {
      SharedPreferences.setMockInitialValues({
        LocalSnapshot.storageKey: jsonEncode({
          'version': 1,
          'values': {
            'profiles': jsonEncode([
              const LocalProfile(
                id: 'default',
                name: '管理员',
                admin: true,
              ).toJson(),
            ]),
            'unknownSetting': 'invalid',
          },
        }),
      });
      final store = testStore(await SharedPreferences.getInstance());
      addTearDown(store.dispose);

      expect(store.configurationError, isNotNull);
      expect(store.locked, isTrue);
      expect(store.sources, isEmpty);
    },
  );
}
