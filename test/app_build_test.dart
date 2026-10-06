import 'dart:convert';

import 'package:duanju_app/app_build.dart';
import 'package:duanju_app/core_bridge.dart';
import 'package:duanju_app/local_profiles.dart';
import 'package:duanju_app/models.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'fixtures.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  const red = FixtureRepository.free;
  const other = FixtureRepository.vip;

  test('edition sources include DSD only in the all-source build', () async {
    SharedPreferences.setMockInitialValues({'source': 'huangdou'});
    final store = testStore(await SharedPreferences.getInstance());
    expect(appSlug, allSourcesEnabled ? 'zhenguojian' : 'hongguojian');
    expect(store.sources.length, allSourcesEnabled ? 62 : 1);
    expect(
      SourceSite.values.any((source) => source.id == 'dsd'),
      allSourcesEnabled,
    );
    expect(SourceSite.isAvailable('dsd'), allSourcesEnabled);
    expect(SourceSite.isKnown('dsd'), isTrue);
    expect(SourceSite.byId('dsd').name, '帝果');
    expect(store.allowsSource('dsd'), allSourcesEnabled);
    expect(store.source, allSourcesEnabled ? 'huangdou' : 'hongguo');
    expect(store.sources.length, allSourcesEnabled ? 62 : 1);
    expect(store.allowsSource('dsd'), allSourcesEnabled);
    store.dispose();
  });

  test(
    'new collectors follow edition gates and keep existing source order',
    () {
      expect(SourceSite.allValues.take(11).map((source) => source.id), [
        'hongguo',
        'hanxiaoquan',
        'guipian',
        'sorani',
        'huangdou',
        'huangju',
        'yeguo',
        'dsd',
        'huangguo-video',
        'huangguoai',
        'cloudfront',
      ]);
      expect(SourceSite.allValues.skip(11).take(2).map((source) => source.id), [
        'liangzi',
        'jciyuan',
      ]);
      expect(SourceSite.collectorValues, hasLength(30));
      expect(
        SourceSite.allValues.map((source) => source.id).toSet(),
        hasLength(62),
      );
      for (final source in SourceSite.collectorValues.map(
        (source) => source.id,
      )) {
        expect(SourceSite.isKnown(source), isTrue);
        expect(SourceSite.isAvailable(source), allSourcesEnabled);
        expect(SourceSite.byId(source).pagedSearch, isTrue);
      }
      expect(SourceSite.isAvailable('xifu'), allSourcesEnabled);
      expect(SourceSite.byId('xifu').onlineSearch, isFalse);
      for (final source in ['wuwu', 'uku', 'zy1080', 'hongdou']) {
        expect(SourceSite.isKnown(source), isFalse);
        expect(SourceSite.isAvailable(source), isFalse);
      }
      final group = SourceGroup.fromSources(
        SourceSite.allValues,
      ).singleWhere((group) => group.id == 'huangguo');
      expect(group.sources.map((source) => source.id), [
        'huangguo-video',
        'huangguoai',
        'cloudfront',
      ]);
    },
  );

  test(
    'edition filtering preserves favorites and history through backup restore',
    () async {
      final history = [
        for (final drama in [red, other])
          WatchEntry(
            drama: drama,
            episode: 1,
            position: 12,
            duration: 60,
            updatedAt: DateTime(2026, 9, 19),
          ).toJson(),
      ];
      SharedPreferences.setMockInitialValues({
        'source': 'huangdou',
        'favorites': jsonEncode([red.toJson(), other.toJson()]),
        'history': jsonEncode(history),
      });
      final store = testStore(await SharedPreferences.getInstance());
      expect(store.favorites.length, allSourcesEnabled ? 2 : 1);
      expect(store.history.length, allSourcesEnabled ? 2 : 1);
      expect(store.isFavorite(other.id), allSourcesEnabled);
      expect(store.watched(other.id) != null, allSourcesEnabled);
      await store.toggleFavorite(red);
      final backup = await store.exportBackup();
      final library =
          (jsonDecode(backup)['libraries'] as Map)['default'] as Map;
      expect((library['favorites'] as List).map((row) => (row as Map)['id']), [
        other.id,
      ]);
      expect(library['history'], hasLength(2));
      await store.importBackup(backup);
      expect(store.preferences.getString('source'), 'huangdou');
      expect(store.history, hasLength(allSourcesEnabled ? 2 : 1));
      expect(
        store.favorites.map((drama) => drama.id),
        allSourcesEnabled ? [other.id] : isEmpty,
      );
      final restoredLibrary =
          (jsonDecode(await store.exportBackup())['libraries']
                  as Map)['default']
              as Map;
      expect(
        (restoredLibrary['favorites'] as List).map((row) => (row as Map)['id']),
        [other.id],
      );
      store.dispose();
    },
  );

  test(
    'a restored foreign-source profile keeps its identity and permissions',
    () async {
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
            sources: ['huangdou'],
            download: false,
          ).toJson(),
        ]),
        'activeProfile': 'viewer',
        'profile.viewer.source': 'huangdou',
      });
      final store = testStore(await SharedPreferences.getInstance());
      expect(store.profile.id, 'viewer');
      expect(store.profile.admin, isFalse);
      expect(store.profile.sources, ['huangdou']);
      expect(store.canDownload, isFalse);
      expect(store.source, allSourcesEnabled ? 'huangdou' : '');
      expect(store.allowsSource('hongguo'), isFalse);
      expect(store.allowsSource('huangdou'), allSourcesEnabled);
      store.dispose();
    },
  );

  test('restored DSD profile data follows edition availability', () async {
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
          name: '帝果旧用户',
          sources: ['dsd'],
          download: false,
        ).toJson(),
      ]),
      'activeProfile': 'viewer',
      'profile.viewer.source': 'dsd',
    });
    final store = testStore(await SharedPreferences.getInstance());
    expect(store.configurationError, isNull);
    expect(store.profile.sources, ['dsd']);
    expect(
      store.sources.map((source) => source.id),
      allSourcesEnabled ? ['dsd'] : [],
    );
    expect(store.source, allSourcesEnabled ? 'dsd' : '');
    expect(store.allowsSource('dsd'), allSourcesEnabled);
    store.dispose();
  });

  test(
    'background requests reject unavailable sources before native I/O',
    () async {
      final repository = NativeRepository(background: true);
      final denied = [
        ...SourceSite.allValues
            .where((source) => !SourceSite.isAvailable(source.id))
            .map((source) => source.id),
        'unknown',
        'wuwu',
        'hongdou',
      ];
      for (final source in denied) {
        final drama = Drama(id: '$source:123', source: source, title: '合成数据');
        final episode = Episode({'id': '1'}, 1);
        for (final request in [
          () => repository.catalog(source),
          () => repository.cached(source),
          () => repository.sourceStatus(source),
          () => repository.startSourceJob(source, 'update'),
          () => repository.cancelSourceJob(source),
          () => repository.detail(drama),
          () => repository.cover(drama),
          () => repository.resolve(drama, episode),
          () => repository.resolveOnline(drama, episode),
          () => repository.enqueueDownloads(DramaDetail(drama, [episode]), [
            episode,
          ]),
          () => repository.localPlayback(drama, episode),
        ]) {
          await expectLater(
            request(),
            throwsA(
              isA<AppFailure>().having(
                (error) => error.message,
                'message',
                '当前版本不包含此站源',
              ),
            ),
          );
        }
      }
    },
  );
}
