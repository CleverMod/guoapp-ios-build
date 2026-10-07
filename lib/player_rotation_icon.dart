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
      ..strokeWidth = 1.7
      ..strokeCap = StrokeCap.round
      ..strokeJoin = StrokeJoin.round;
    canvas.drawRRect(
      RRect.fromRectAndRadius(
        const Rect.fromLTWH(8, 7, 8, 10.5),
        const Radius.circular(1.2),
      ),
      stroke,
    );
    canvas.drawPath(
      Path()
        ..moveTo(18.5, 7.3)
        ..lineTo(18.5, 5.8)
        ..quadraticBezierTo(18.5, 3.9, 16.5, 3.9)
        ..lineTo(13.7, 3.9)
        ..moveTo(15.7, 2.3)
        ..lineTo(13.7, 3.9)
        ..lineTo(15.7, 5.5)
        ..moveTo(5.5, 16.7)
        ..lineTo(5.5, 18.2)
        ..quadraticBezierTo(5.5, 20.1, 7.5, 20.1)
        ..lineTo(10.3, 20.1)
        ..moveTo(8.3, 18.5)
        ..lineTo(10.3, 20.1)
        ..lineTo(8.3, 21.7),
      stroke,
    );
    canvas.restore();
  }

  @override
  bool shouldRepaint(_RotationPainter oldDelegate) =>
      color != oldDelegate.color;
}
