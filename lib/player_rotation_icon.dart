import 'package:flutter/material.dart';

class PlayerRotationIcon extends StatelessWidget {
  const PlayerRotationIcon({super.key});

  @override
  Widget build(BuildContext context) {
    final theme = IconTheme.of(context);
    return SizedBox.square(
      dimension: theme.size ?? 24,
      child: CustomPaint(
        painter: _RotationPainter(theme.color ?? Colors.white),
      ),
    );
  }
}

class _RotationPainter extends CustomPainter {
  const _RotationPainter(this.color);

  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    canvas.save();
    canvas.scale(size.width / 24, size.height / 24);
    final stroke = Paint()
      ..color = color
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.4
      ..strokeCap = StrokeCap.round
      ..strokeJoin = StrokeJoin.round;
    canvas.drawRRect(
      RRect.fromRectAndRadius(
        const Rect.fromLTWH(8.7, 7.3, 6.6, 9),
        const Radius.circular(.8),
      ),
      stroke,
    );
    stroke.strokeWidth = 1.2;
    canvas.drawPath(
      Path()
        ..moveTo(18, 7.2)
        ..lineTo(18, 6.3)
        ..quadraticBezierTo(18, 4.1, 15.7, 4.1)
        ..lineTo(14.8, 4.1)
        ..moveTo(6, 16.8)
        ..lineTo(6, 17.7)
        ..quadraticBezierTo(6, 19.9, 8.3, 19.9)
        ..lineTo(9.2, 19.9),
      stroke,
    );
    canvas.drawPath(
      Path()
        ..moveTo(13.5, 4.1)
        ..lineTo(15.3, 2.6)
        ..lineTo(15.3, 5.6)
        ..close()
        ..moveTo(10.5, 19.9)
        ..lineTo(8.7, 18.4)
        ..lineTo(8.7, 21.4)
        ..close(),
      Paint()..color = color,
    );
    canvas.restore();
  }

  @override
  bool shouldRepaint(_RotationPainter oldDelegate) =>
      color != oldDelegate.color;
}
