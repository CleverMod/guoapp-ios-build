import 'dart:async';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:duanju_app/app_layout.dart';
import 'package:duanju_app/player_controls.dart';
import 'package:duanju_app/player_feedback.dart';
import 'package:duanju_app/player_interactions.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:media_kit/media_kit.dart';

import 'player_fixtures.dart';

class FeedbackPlayer extends ScriptedPlayer {
  @override
  Future<void> play() async {
    state = state.copyWith(playing: true);
    playingController.add(true);
  }
}

class FeedbackOwner extends StatefulWidget {
  const FeedbackOwner({
    super.key,
    required this.player,
    required this.interactions,
    required this.child,
  });

  final Player player;
  final PlayerInteractions interactions;
  final Widget child;

  @override
  State<FeedbackOwner> createState() => _FeedbackOwnerState();
}

class _FeedbackOwnerState extends State<FeedbackOwner> {
  @override
  Widget build(BuildContext context) => widget.child;

  @override
  void dispose() {
    widget.interactions.dispose();
    unawaited(widget.player.dispose());
    super.dispose();
  }
}

void main() {
  Future<void> capture(WidgetTester tester, String name) async {
    final directory = Platform.environment['PLAYER_FEEDBACK_SCREENSHOTS'];
    if (directory == null) return;
    final boundary = tester.renderObject<RenderRepaintBoundary>(
      find.byKey(const ValueKey('feedback-capture')),
    );
    await tester.runAsync(() async {
      final image = await boundary.toImage();
      final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
      final output = File('$directory/$name.png');
      await output.parent.create(recursive: true);
      await output.writeAsBytes(bytes!.buffer.asUint8List());
      image.dispose();
    });
  }

  Future<(FeedbackPlayer, PlayerInteractions)> mount(
    WidgetTester tester, {
    Size size = const Size(844, 390),
    bool mobile = true,
  }) async {
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final platform = FeedbackPlayer();
    await platform.open(Media('https://media.test/synthetic.mp4'));
    await platform.seek(const Duration(seconds: 20));
    await platform.setRate(1.5);
    final player = Player(platformPlayer: platform);
    final interactions = PlayerInteractions(
      player: player,
      available: () => true,
      baseSpeed: () => 1.5,
      onTogglePlayback: () => player.playOrPause(),
      onFullscreen: () {},
      onEpisode: (_) => '',
    );
    final fontPath = Platform.environment['PLAYER_FEEDBACK_FONT'];
    String? fontFamily;
    await tester.runAsync(() async {
      final icons = FontLoader('MaterialIcons')
        ..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'));
      await icons.load();
    });
    if (fontPath != null && File(fontPath).existsSync()) {
      await tester.runAsync(() async {
        final loader = FontLoader('FeedbackTest')
          ..addFont(File(fontPath).readAsBytes().then(ByteData.sublistView));
        await loader.load();
      });
      fontFamily = 'FeedbackTest';
    }
    await tester.pumpWidget(
      MaterialApp(
        theme: ThemeData.dark().copyWith(
          textTheme: ThemeData.dark().textTheme.apply(fontFamily: fontFamily),
        ),
        home: FeedbackOwner(
          player: player,
          interactions: interactions,
          child: Scaffold(
            body: RepaintBoundary(
              key: const ValueKey('feedback-capture'),
              child: Stack(
                fit: StackFit.expand,
                children: [
                  const DecoratedBox(
                    decoration: BoxDecoration(
                      gradient: LinearGradient(
                        colors: [Color(0xFF687B70), Color(0xFFC9BD91)],
                      ),
                    ),
                  ),
                  const Align(
                    alignment: Alignment(-.35, .15),
                    child: Icon(Icons.landscape_rounded, size: 180),
                  ),
                  PlayerControls(
                    player: player,
                    interactions: interactions,
                    enabled: true,
                    fullscreen: true,
                    showOnPlaybackReady: true,
                    onFullscreen: () {},
                    onBack: () {},
                    onPrevious: null,
                    onNext: null,
                    title: '合成播放画面',
                    onTogglePlayback: () => player.playOrPause(),
                    onEpisodes: () async {},
                    onSpeed: () async {},
                    onQuality: () async {},
                    onSettings: () async {},
                    speed: 1.5,
                    qualityLabel: '自动',
                    onFocusSurface: () {},
                    swipeEnabled: mobile,
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pump();
    return (platform, interactions);
  }

  double controlsOpacity(WidgetTester tester) => tester
      .widget<AnimatedOpacity>(
        find.byKey(const ValueKey('player-controls-overlay')),
      )
      .opacity;

  testWidgets(
    'updates do not extend feedback and a new operation restarts it',
    (tester) async {
      final feedback = PlayerFeedbackController();
      addTearDown(feedback.dispose);
      feedback.show('音量 50%', icon: Icons.volume_up_rounded);
      await tester.pump(const Duration(milliseconds: 1500));
      feedback.update('音量 70%', icon: Icons.volume_up_rounded);
      expect(feedback.message, '音量 70%');
      await tester.pump(const Duration(milliseconds: 499));
      expect(feedback.visible, isTrue);
      await tester.pump(const Duration(milliseconds: 1));
      expect(feedback.visible, isFalse);
      feedback.update('音量 80%', icon: Icons.volume_up_rounded);
      expect(feedback.visible, isFalse);
      feedback.show('后退 10 秒', icon: Icons.fast_rewind_rounded);
      await tester.pump(const Duration(seconds: 1));
      feedback.show('前进 10 秒', icon: Icons.fast_forward_rounded);
      await tester.pump(const Duration(milliseconds: 1999));
      expect(feedback.visible, isTrue);
      await tester.pump(const Duration(milliseconds: 1));
      expect(feedback.visible, isFalse);
    },
  );

  testWidgets('held boost hides after two seconds and release restores speed', (
    tester,
  ) async {
    final (player, interactions) = await mount(tester);
    final gesture = await tester.startGesture(const Offset(700, 160));
    await tester.pump(const Duration(milliseconds: 350));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 180));
    expect(player.state.rate, 3);
    expect(find.text('3.0 X'), findsOneWidget);
    expect(controlsOpacity(tester), 0);
    final rect = tester.getRect(find.byKey(const ValueKey('player-feedback')));
    expect(rect.center.dx, closeTo(422, 1));
    expect(rect.top, greaterThanOrEqualTo(24));
    expect(rect.bottom, lessThan(60));
    await capture(tester, 'speed');
    await tester.pump(const Duration(milliseconds: 1820));
    expect(find.byKey(const ValueKey('player-feedback')), findsNothing);
    expect(interactions.boosting, isTrue);
    expect(player.state.rate, 3);
    expect(controlsOpacity(tester), 0);
    await tester.pump(const Duration(seconds: 4));
    expect(player.state.rate, 3);
    await capture(tester, 'clean-after-two-seconds');
    await gesture.up();
    await tester.pump();
    await interactions.pendingRates;
    expect(player.state.rate, 1.5);
    expect(interactions.boosting, isFalse);
    expect(find.byKey(const ValueKey('player-feedback')), findsNothing);
    expect(controlsOpacity(tester), 0);
  });

  testWidgets('double tap seeks with a top hint and keeps controls hidden', (
    tester,
  ) async {
    final (player, interactions) = await mount(tester);
    final surface = find.byKey(const ValueKey('player-gesture-surface'));
    await tester.tapAt(const Offset(700, 160));
    await tester.pump(const Duration(milliseconds: 100));
    await tester.tapAt(const Offset(700, 160));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 180));
    expect(player.state.position, const Duration(seconds: 30));
    expect(find.text('前进 10 秒'), findsOneWidget);
    expect(controlsOpacity(tester), 0);
    await capture(tester, 'seek');
    await tester.pump(const Duration(milliseconds: 1820));
    expect(find.byKey(const ValueKey('player-feedback')), findsNothing);
    expect(controlsOpacity(tester), 0);
    interactions.doubleTap(100, tester.getSize(surface).width, mobile: true);
    await tester.pump();
    expect(player.state.position, const Duration(seconds: 20));
    expect(find.text('后退 10 秒'), findsOneWidget);
  });

  testWidgets('continued brightness gesture cannot revive expired feedback', (
    tester,
  ) async {
    final (_, interactions) = await mount(tester);
    final gesture = await tester.startGesture(const Offset(100, 180));
    await gesture.moveBy(const Offset(0, -25));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 180));
    expect(interactions.feedback.message, startsWith('亮度 '));
    expect(controlsOpacity(tester), 0);
    await capture(tester, 'brightness');
    await tester.pump(const Duration(milliseconds: 1820));
    final before = interactions.brightness;
    await gesture.moveBy(const Offset(0, -30));
    await tester.pump();
    expect(interactions.brightness, greaterThan(before));
    expect(interactions.feedback.visible, isFalse);
    expect(controlsOpacity(tester), 0);
    await gesture.up();
    await tester.pump();
    expect(interactions.operating, isFalse);
    await tester.pump(const Duration(milliseconds: 700));
    final next = await tester.startGesture(const Offset(100, 180));
    await next.moveBy(const Offset(0, 25));
    await tester.pump();
    expect(interactions.feedback.visible, isTrue);
    await next.up();
    await tester.pump(const Duration(milliseconds: 50));
  });

  testWidgets('iOS system volume keeps changing after its feedback expires', (
    tester,
  ) async {
    final originalPlatform = debugDefaultTargetPlatformOverride;
    debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
    final messenger = tester.binding.defaultBinaryMessenger;
    var systemVolume = .6;
    messenger.setMockMethodCallHandler(AppDevice.channel, (call) async {
      if (call.method == 'getMediaVolume') return systemVolume;
      if (call.method == 'getBrightness') return .5;
      if (call.method == 'setMediaVolume') {
        systemVolume = (call.arguments as Map)['volume'] as double;
      }
      return null;
    });
    const events = MethodChannel('duanju/media_volume');
    messenger.setMockMethodCallHandler(events, (_) async => null);
    addTearDown(() {
      messenger.setMockMethodCallHandler(AppDevice.channel, null);
      messenger.setMockMethodCallHandler(events, null);
    });
    try {
      final (player, interactions) = await mount(tester);
      final gesture = await tester.startGesture(const Offset(700, 180));
      await tester.pump();
      await gesture.moveBy(const Offset(0, -25));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 180));
      expect(interactions.feedback.message, startsWith('音量 '));
      expect(systemVolume, greaterThan(.6));
      expect(player.state.volume, 100);
      await capture(tester, 'volume');
      await tester.pump(const Duration(milliseconds: 1820));
      final before = systemVolume;
      await gesture.moveBy(const Offset(0, -30));
      await tester.pump();
      expect(systemVolume, greaterThan(before));
      expect(interactions.feedback.visible, isFalse);
      expect(controlsOpacity(tester), 0);
      await gesture.up();
      await tester.pump();
    } finally {
      debugDefaultTargetPlatformOverride = originalPlatform;
    }
  });

  for (final playing in [true, false]) {
    testWidgets('scrub feedback expires and preserves playing=$playing', (
      tester,
    ) async {
      final (player, interactions) = await mount(tester);
      if (!playing) {
        await player.pause();
        await tester.pump();
      }
      interactions.beginScrub();
      interactions.updateScrub(const Duration(seconds: 35));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 180));
      expect(player.state.playing, isFalse);
      expect(player.state.position, const Duration(seconds: 35));
      expect(interactions.feedback.visible, isTrue);
      expect(controlsOpacity(tester), 0);
      if (playing) await capture(tester, 'scrub');
      await tester.pump(const Duration(milliseconds: 1820));
      interactions.updateScrub(const Duration(seconds: 45));
      await tester.pump(const Duration(milliseconds: 150));
      expect(player.state.position, const Duration(seconds: 45));
      expect(interactions.feedback.visible, isFalse);
      expect(controlsOpacity(tester), 0);
      interactions.endScrub();
      await tester.pump();
      await tester.pump();
      expect(interactions.scrubbing, isFalse);
      expect(player.state.playing, playing);
      expect(player.state.position, const Duration(seconds: 45));
      if (playing) expect(controlsOpacity(tester), 0);
    });
  }

  testWidgets('timeline drag continues after the control overlay is hidden', (
    tester,
  ) async {
    final (player, interactions) = await mount(tester);
    final timeline = tester.getRect(
      find.byKey(const ValueKey('player-progress')),
    );
    final gesture = await tester.startGesture(
      Offset(timeline.left + timeline.width * .3, timeline.center.dy),
    );
    await gesture.moveBy(const Offset(30, 0));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 180));
    expect(interactions.scrubbing, isTrue);
    expect(controlsOpacity(tester), 0);
    await tester.pump(const Duration(seconds: 2));
    final previous = player.state.position;
    await gesture.moveBy(const Offset(100, 0));
    await tester.pump(const Duration(milliseconds: 150));
    expect(player.state.position, greaterThan(previous));
    expect(interactions.feedback.visible, isFalse);
    await gesture.up();
    await tester.pump();
    await tester.pump();
    expect(interactions.scrubbing, isFalse);
    expect(player.state.playing, isTrue);
    expect(controlsOpacity(tester), 0);
  });

  testWidgets('horizontal swipe previews keep working after feedback expires', (
    tester,
  ) async {
    final (player, interactions) = await mount(tester);
    final gesture = await tester.startGesture(const Offset(400, 100));
    await gesture.moveBy(const Offset(50, 0));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 180));
    expect(interactions.scrubbing, isTrue);
    expect(player.state.playing, isFalse);
    expect(controlsOpacity(tester), 0);
    await tester.pump(const Duration(seconds: 2));
    final previous = player.state.position;
    await gesture.moveBy(const Offset(60, 0));
    await tester.pump(const Duration(milliseconds: 150));
    expect(player.state.position, greaterThan(previous));
    expect(interactions.feedback.visible, isFalse);
    await gesture.up();
    await tester.pump();
    await tester.pump();
    expect(interactions.scrubbing, isFalse);
    expect(player.state.playing, isTrue);
    expect(controlsOpacity(tester), 0);
  });

  testWidgets('desktop key repeat adjusts volume without extending feedback', (
    tester,
  ) async {
    final (player, interactions) = await mount(tester, mobile: false);
    await player.setVolume(50);
    interactions.changeVolume(5);
    await tester.pump(const Duration(seconds: 1));
    interactions.key(
      const KeyRepeatEvent(
        physicalKey: PhysicalKeyboardKey.arrowUp,
        logicalKey: LogicalKeyboardKey.arrowUp,
        timeStamp: Duration.zero,
      ),
    );
    await tester.pump(const Duration(seconds: 1));
    expect(player.state.volume, 60);
    expect(interactions.feedback.visible, isFalse);
    interactions.changeVolume(5, repeat: true);
    await tester.pump();
    expect(player.state.volume, 65);
    expect(interactions.feedback.visible, isFalse);
    interactions.changeVolume(5);
    await tester.pump();
    expect(interactions.feedback.visible, isTrue);
  });

  testWidgets(
    'compact feedback fits the safe area and leaves gestures usable',
    (tester) async {
      final (_, interactions) = await mount(tester, size: const Size(320, 180));
      tester.view.padding = const FakeViewPadding(top: 20, left: 24);
      interactions.seek(10);
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 180));
      final rect = tester.getRect(
        find.byKey(const ValueKey('player-feedback')),
      );
      expect(rect.left, greaterThanOrEqualTo(24));
      expect(rect.right, lessThanOrEqualTo(304));
      expect(rect.top, greaterThanOrEqualTo(24));
      expect(controlsOpacity(tester), 0);
      expect(tester.takeException(), isNull);
      await capture(tester, 'compact');
    },
  );
}
