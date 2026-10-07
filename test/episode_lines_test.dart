import 'dart:io';
import 'dart:ui' as ui;

import 'package:duanju_app/batch_downloads.dart';
import 'package:duanju_app/detail_screen.dart';
import 'package:duanju_app/download_picker.dart';
import 'package:duanju_app/follow_state.dart';
import 'package:duanju_app/local_store.dart';
import 'package:duanju_app/models.dart';
import 'package:duanju_app/player_menu.dart';
import 'package:duanju_app/player_screen.dart';
import 'package:duanju_app/playback_preloader.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:media_kit/media_kit.dart';

import 'fixtures.dart';
import 'player_fixtures.dart';

List<Episode> lineEpisodes(String line, int count) => [
  for (var number = 1; number <= count; number++)
    Episode({
      'id': 'hongguo:lines:$line-$number',
      'title': '第$number集',
      'currentEpisode': number,
      'lineId': line,
      'lineName': '线路 $line',
    }, number),
];

DramaDetail lineDetail(List<int> counts) => DramaDetail(
  const Drama(
    id: 'hongguo:lines',
    source: 'hongguo',
    title: '合成多线路',
    episodes: 7,
  ),
  [
    for (var index = 0; index < counts.length; index++)
      ...lineEpisodes('${index + 1}', counts[index]),
  ],
);

class LineRepository extends FixtureRepository {
  LineRepository(this.value);
  final DramaDetail value;
  @override
  Future<DramaDetail> detail(Drama drama) async => value;
  @override
  Future<Drama> supplementMetadata(Drama drama) async => drama;
}

class LinePlayerRepository extends RouteRepository {
  LinePlayerRepository(this.value);
  final DramaDetail value;
  @override
  Future<DramaDetail> detail(Drama drama) async => value;
  @override
  Future<PlaybackPlan?> preload(
    Drama drama,
    Episode episode, {
    int quality = 0,
    bool online = false,
  }) async => const PlaybackPlan(
    url: 'https://media.test/preloaded.mp4',
    session: 'preloaded',
  );
}

void main() {
  Future<LocalStore> store() async {
    SharedPreferences.setMockInitialValues({});
    final value = LocalStore(await SharedPreferences.getInstance());
    addTearDown(value.dispose);
    return value;
  }

  void phone(WidgetTester tester) {
    tester.view.physicalSize = const Size(393, 852);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
  }

  Future<void> chooseLine(WidgetTester tester, String key, String label) async {
    await tester.tap(find.byKey(ValueKey(key)));
    await tester.pumpAndSettle();
    await tester.tap(find.text(label).last);
    await tester.pumpAndSettle();
  }

  test('next and previous episodes never cross source lines', () {
    final detail = lineDetail([7, 7, 8]);
    expect(detail.lines.map((line) => line.episodes.length), [7, 7, 8]);
    expect(detail.defaultEpisodes.length, 7);
    expect(episodeNeighborIndex(detail.episodes, 6, 1), -1);
    expect(episodeNeighborIndex(detail.episodes, 7, -1), -1);
    expect(episodeNeighborIndex(detail.episodes, 7, 1), 8);
    expect(episodeNeighborIndex(detail.episodes, 21, 1), -1);
    final movie = lineDetail([1, 1, 1]);
    expect(episodeNeighborIndex(movie.episodes, 0, 1), -1);
  });

  test(
    'resume and history refresh preserve the actual line and chapter',
    () async {
      final detail = lineDetail([7, 7, 8]);
      final entry = WatchEntry(
        drama: detail.drama,
        episode: 7,
        episodeId: detail.episodes[13].id,
        lineId: '2',
        position: 15,
        duration: 60,
        updatedAt: DateTime.utc(2026, 10, 7),
      );
      final local = await store();
      await local.saveWatch(entry);
      await local.saveWatch(entry);
      await local.refreshDrama(detail.drama);
      expect(local.watched(detail.drama.id)!.lineId, '2');
      final restored = WatchEntry.fromJson(entry.toJson());
      expect(resumeEpisodeIndex(detail.episodes, restored), 13);
      final finished = WatchEntry.fromJson({...entry.toJson(), 'position': 60});
      expect(resumeEpisodeIndex(detail.episodes, finished), 13);
      final old = WatchEntry.fromJson(
        {...entry.toJson()}
          ..remove('episodeId')
          ..remove('lineId'),
      );
      expect(resumeEpisodeIndex(detail.episodes, old), 6);
    },
  );

  test('batch downloads use a single default line', () {
    final detail = lineDetail([7, 7, 8]);
    final batch = BatchDownloadItem(detail.drama)..detail = detail;
    expect(batch.episodes(true).map((episode) => episode.lineId).toSet(), {
      '1',
    });
    expect(batch.episodes(true).length, 7);
  });

  testWidgets(
    'preloaded media is never reused for another line with the same number',
    (tester) async {
      final detail = lineDetail([1, 1, 1]);
      final repository = LinePlayerRepository(detail);
      final preloader = PlaybackPreloader(repository);
      preloader.prepare(detail.drama, detail.episodes[0]);
      await tester.pump(const Duration(milliseconds: 650));
      await tester.pump();
      expect(preloader.status, '下一集播放地址已准备');
      expect(preloader.take(detail.drama, detail.episodes[1]), isNull);
      preloader.dispose();
      await tester.pump();
    },
  );

  testWidgets(
    'finishing a movie never automatically plays the next source line',
    (tester) async {
      phone(tester);
      debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
      addTearDown(() => debugDefaultTargetPlatformOverride = null);
      final detail = lineDetail([1, 1, 1]);
      final repository = LinePlayerRepository(detail);
      final player = ScriptedPlayer();
      final local = await store();
      await tester.pumpWidget(
        MaterialApp(
          home: PlayerScreen(
            detail: detail,
            initialIndex: 0,
            repository: repository,
            store: local,
            playerFactory: () => Player(platformPlayer: player),
            videoBuilder: (controls) => controls,
          ),
        ),
      );
      for (var i = 0; i < 12; i++) {
        await tester.pump(const Duration(milliseconds: 10));
      }
      expect(repository.primaryCalls, 1);
      player.finishEpisode();
      for (var i = 0; i < 12; i++) {
        await tester.pump(const Duration(milliseconds: 10));
      }
      expect(repository.primaryCalls, 1);
      expect(local.watched(detail.drama.id)?.lineId, '1');
      await tester.pumpWidget(const SizedBox.shrink());
      for (var i = 0; i < 12; i++) {
        await tester.pump(const Duration(milliseconds: 10));
      }
      expect(player.disposed, isTrue);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('movie alternatives show one 1 and one current button', (
    tester,
  ) async {
    phone(tester);
    final detail = lineDetail([1, 1, 1]);
    int? selected;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: PlayerEpisodeGrid(
            episodes: detail.episodes,
            currentIndex: 0,
            compact: true,
            onSelected: (index) => selected = index,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('1'), findsOneWidget);
    expect(
      find.byKey(ValueKey('play-episode-${detail.episodes[0].browserKey}')),
      findsOneWidget,
    );
    await chooseLine(tester, 'play-episode-line-1', '线路 2 · 1 集');
    expect(selected, 1);
    expect(find.text('1'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('series alternatives show only the selected 7 or 8 episodes', (
    tester,
  ) async {
    phone(tester);
    final detail = lineDetail([7, 7, 8]);
    int? selected;
    await tester.pumpWidget(
      MaterialApp(
        home: RepaintBoundary(
          key: const ValueKey('line-capture'),
          child: Scaffold(
            body: Column(
              children: [
                const SizedBox(
                  height: 235,
                  child: ColoredBox(
                    color: Colors.black,
                    child: Center(
                      child: Text(
                        '合成播放器',
                        style: TextStyle(color: Colors.white),
                      ),
                    ),
                  ),
                ),
                Expanded(
                  child: PlayerEpisodeGrid(
                    episodes: detail.episodes,
                    currentIndex: 0,
                    compact: true,
                    onSelected: (index) => selected = index,
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('1'), findsOneWidget);
    expect(find.text('7'), findsOneWidget);
    expect(find.text('8'), findsNothing);
    final path = Platform.environment['EPISODE_LINE_SCREENSHOT'];
    if (path != null) {
      final boundary = tester.renderObject<RenderRepaintBoundary>(
        find.byKey(const ValueKey('line-capture')),
      );
      await tester.runAsync(() async {
        final image = await boundary.toImage();
        final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
        final output = File(path);
        await output.parent.create(recursive: true);
        await output.writeAsBytes(bytes!.buffer.asUint8List());
        image.dispose();
      });
    }
    await chooseLine(tester, 'play-episode-line-1', '线路 3 · 8 集');
    expect(selected, 14);
    expect(find.text('1'), findsOneWidget);
    expect(find.text('8'), findsOneWidget);
    await tester.tap(
      find.byKey(ValueKey('play-episode-${detail.episodes[21].browserKey}')),
    );
    expect(selected, 21);
    expect(tester.takeException(), isNull);
  });

  testWidgets('detail browsing and download selection retain one route', (
    tester,
  ) async {
    phone(tester);
    final detail = lineDetail([7, 7, 8]);
    final local = await store();
    await tester.pumpWidget(
      MaterialApp(
        home: DetailScreen(
          drama: detail.drama,
          repository: LineRepository(detail),
          store: local,
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('播放线路'), findsOneWidget);
    await chooseLine(tester, 'detail-line-1', '线路 3 · 8 集');
    expect(find.textContaining('选集 · 8 集'), findsOneWidget);
    DownloadSelection? selection;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: DownloadPicker(
            detail: detail,
            initialLineId: '2',
            embedded: true,
            onSubmit: (value) async {
              selection = value;
            },
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('1'), findsOneWidget);
    await chooseLine(tester, 'download-line-2', '线路 3 · 8 集');
    await tester.tap(find.byKey(const ValueKey('enqueue-downloads')));
    await tester.pumpAndSettle();
    expect(selection!.episodes.length, 8);
    expect(selection!.episodes.map((episode) => episode.lineId).toSet(), {'3'});
    expect(tester.takeException(), isNull);
  });
}
