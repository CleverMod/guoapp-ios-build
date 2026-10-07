import 'dart:async';

import 'package:flutter/material.dart';

class PlayerFeedbackController extends ChangeNotifier {
  static const displayDuration = Duration(seconds: 2);

  Timer? _timer;
  String _message = '';
  IconData _icon = Icons.info_outline_rounded;
  bool _visible = false;

  String get message => _message;
  IconData get icon => _icon;
  bool get visible => _visible;

  void show(String message, {IconData icon = Icons.info_outline_rounded}) {
    if (message.isEmpty) {
      clear();
      return;
    }
    _timer?.cancel();
    _message = message;
    _icon = icon;
    _visible = true;
    _timer = Timer(displayDuration, clear);
    notifyListeners();
  }

  void update(String message, {required IconData icon}) {
    if (!_visible || (_message == message && _icon == icon)) return;
    _message = message;
    _icon = icon;
    notifyListeners();
  }

  void clear() {
    _timer?.cancel();
    _timer = null;
    if (!_visible) return;
    _visible = false;
    _message = '';
    notifyListeners();
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }
}

class PlayerFeedbackOverlay extends StatelessWidget {
  const PlayerFeedbackOverlay({
    super.key,
    required this.feedback,
    this.fontSize = 18,
  });

  final PlayerFeedbackController feedback;
  final double fontSize;

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: feedback,
    builder: (context, _) {
      if (!feedback.visible) return const SizedBox.shrink();
      const shadows = [
        Shadow(color: Colors.black87, blurRadius: 4, offset: Offset(0, 1)),
      ];
      return IgnorePointer(
        child: SafeArea(
          minimum: const EdgeInsets.fromLTRB(16, 24, 16, 0),
          bottom: false,
          child: Align(
            alignment: Alignment.topCenter,
            child: Row(
              key: const ValueKey('player-feedback'),
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  feedback.icon,
                  color: Colors.white,
                  size: fontSize + 6,
                  shadows: shadows,
                ),
                const SizedBox(width: 8),
                Flexible(
                  child: Text(
                    feedback.message,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    textAlign: TextAlign.center,
                    style: TextStyle(
                      color: Colors.white,
                      fontSize: fontSize,
                      fontWeight: FontWeight.w600,
                      height: 1.2,
                      shadows: shadows,
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      );
    },
  );
}
