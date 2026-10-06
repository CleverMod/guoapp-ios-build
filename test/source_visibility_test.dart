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
    expect(store.sources, hasLength(allSourcesEnabled ? 62 : 1));
    for (final source in sourceIds) {
      expect(store.allowsSource(source), isTrue);
    }
  });

  test('retired attached source permissions are removed during migration', () {
    final profile = LocalProfile.fromJson(
      const LocalProfile(
        id: 'viewer',
        name: '已有用户',
        sources: ['yeguo-worker', 'wuwu', 'xingya'],
      ).toJson(),
    );
    expect(profile.sources, ['xingya']);
    expect(SourceSite.isKnown('yeguo-worker'), isFalse);
  });

  test(
    'removed source permissions preserve profiles and backup records',
    () async {
      const retired = Drama(
        id: 'hongdou:123',
        source: 'hongdou',
        sourceId: '123',
        title: '合成旧记录',
      );
      final history = WatchEntry(
        drama: retired,
        episode: 1,
        position: 12,
        duration: 60,
        updatedAt: DateTime(2026, 10, 5),
      );
      SharedPreferences.setMockInitialValues({
        'profiles': jsonEncode([
          LocalProfile(
            id: 'default',
            name: '管理员',
            admin: true,
            salt: '0' * 32,
            pinHash: '1' * 64,
          ).toJson(),
          const LocalProfile(
            id: 'viewer',
            name: '已有用户',
            sources: ['hongdou', 'hongguo', 'uku', 'xifu'],
          ).toJson(),
        ]),
        'activeProfile': 'default',
        'forceLogin': false,
        'source': 'hongdou',
        'profile.viewer.source': 'hongdou',
        'profile.viewer.favorites': jsonEncode([retired.toJson()]),
        'profile.viewer.history': jsonEncode([history.toJson()]),
      });
      final store = testStore(await SharedPreferences.getInstance());
      addTearDown(store.dispose);
      expect(store.configurationError, isNull);
      expect(store.profile.id, 'default');
      expect(
        store.profiles.firstWhere((profile) => profile.id == 'viewer').sources,
        ['hongguo', 'xifu'],
      );
      expect(store.source, 'hongguo');
      expect(store.favorites, isEmpty);
      expect(store.history, isEmpty);
      expect(store.allowsSource('hongdou'), isFalse);
      expect(store.allowsSource('uku'), isFalse);
      final backup = jsonDecode(await store.exportBackup()) as Map;
      final library = (backup['libraries'] as Map)['viewer'] as Map;
      expect((library['favorites'] as List).single['id'], retired.id);
      expect((library['history'] as List).single['drama']['id'], retired.id);
      await store.importBackup(jsonEncode(backup));
      await store.switchProfile('viewer');
      expect(store.configurationError, isNull);
      expect(store.source, 'hongguo');
      expect(store.profile.sources, ['hongguo', 'xifu']);
      expect(store.favorites, isEmpty);
      expect(store.history, isEmpty);
      expect(
        () => LocalProfile.fromJson({
          ...store.profile.toJson(),
          'sources': ['unregistered-source'],
        }),
        throwsFormatException,
      );
    },
  );

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
