import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

abstract final class AppTheme {
  static final light = _theme(Brightness.light);
  static final dark = _theme(Brightness.dark);

  static ThemeMode mode(String preference) => switch (preference) {
    'light' => ThemeMode.light,
    'dark' => ThemeMode.dark,
    _ => ThemeMode.system,
  };

  static String label(String preference) => switch (preference) {
    'light' => '浅色',
    'dark' => '深色',
    _ => '跟随系统',
  };

  static SystemUiOverlayStyle systemBars(Brightness brightness) {
    final icons = brightness == Brightness.dark
        ? Brightness.light
        : Brightness.dark;
    return SystemUiOverlayStyle(
      statusBarColor: Colors.transparent,
      statusBarIconBrightness: icons,
      statusBarBrightness: brightness,
      systemStatusBarContrastEnforced: false,
      systemNavigationBarColor: Colors.transparent,
      systemNavigationBarDividerColor: Colors.transparent,
      systemNavigationBarIconBrightness: icons,
      systemNavigationBarContrastEnforced: false,
    );
  }

  static ThemeData _theme(Brightness brightness) {
    final dark = brightness == Brightness.dark;
    final background = dark ? const Color(0xFF0E1014) : const Color(0xFFF6F7F9);
    final scheme = ColorScheme.fromSeed(
      seedColor: const Color(0xFFFF664F),
      brightness: brightness,
      primary: dark ? const Color(0xFFFF765F) : const Color(0xFFAD3826),
      onPrimary: dark ? const Color(0xFF3B0E07) : Colors.white,
      primaryContainer: dark
          ? const Color(0xFF5C3027)
          : const Color(0xFFFFE0D8),
      onPrimaryContainer: dark
          ? const Color(0xFFFFE0D8)
          : const Color(0xFF4B160D),
      tertiary: dark ? const Color(0xFFF6C86B) : const Color(0xFF805500),
      surface: dark ? const Color(0xFF16191F) : Colors.white,
      onSurface: dark ? const Color(0xFFF3F4F7) : const Color(0xFF20242C),
      onSurfaceVariant: dark
          ? const Color(0xFFA6ACB9)
          : const Color(0xFF697180),
      outline: dark ? const Color(0xFF85818B) : const Color(0xFF7C757D),
      outlineVariant: dark ? const Color(0xFF303640) : const Color(0xFFDFE3E9),
      surfaceContainerLowest: dark ? const Color(0xFF0D0E11) : Colors.white,
      surfaceContainerLow: dark ? const Color(0xFF181C23) : Colors.white,
      surfaceContainer: dark
          ? const Color(0xFF1E232C)
          : const Color(0xFFEEF0F4),
      surfaceContainerHigh: dark
          ? const Color(0xFF262D37)
          : const Color(0xFFE8EBF0),
      surfaceContainerHighest: dark
          ? const Color(0xFF303946)
          : const Color(0xFFE1E5EC),
      surfaceTint: Colors.transparent,
    );
    return ThemeData(
      useMaterial3: true,
      brightness: brightness,
      colorScheme: scheme,
      scaffoldBackgroundColor: background,
      textTheme: const TextTheme(
        headlineSmall: TextStyle(
          fontSize: 27,
          height: 1.2,
          fontWeight: FontWeight.w800,
          letterSpacing: -.6,
        ),
        titleLarge: TextStyle(
          fontSize: 23,
          height: 1.25,
          fontWeight: FontWeight.w700,
          letterSpacing: -.3,
        ),
        titleMedium: TextStyle(fontWeight: FontWeight.w600),
        titleSmall: TextStyle(fontWeight: FontWeight.w600),
        bodyLarge: TextStyle(height: 1.5),
        bodyMedium: TextStyle(height: 1.5),
        bodySmall: TextStyle(height: 1.4),
        labelLarge: TextStyle(fontWeight: FontWeight.w600),
      ),
      appBarTheme: AppBarTheme(
        backgroundColor: background,
        foregroundColor: scheme.onSurface,
        scrolledUnderElevation: 0,
        elevation: 0,
        centerTitle: false,
        titleTextStyle: TextStyle(
          color: scheme.onSurface,
          fontSize: 22,
          fontWeight: FontWeight.w700,
          letterSpacing: -.3,
        ),
        systemOverlayStyle: systemBars(brightness),
      ),
      dividerTheme: DividerThemeData(
        color: scheme.outlineVariant.withValues(alpha: .6),
        thickness: .7,
      ),
      cardTheme: CardThemeData(
        color: scheme.surfaceContainerLow,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(22)),
      ),
      navigationRailTheme: NavigationRailThemeData(
        backgroundColor: background,
        useIndicator: true,
        indicatorColor: scheme.primary.withValues(alpha: .12),
        indicatorShape: const StadiumBorder(),
        selectedIconTheme: IconThemeData(color: scheme.primary),
        unselectedIconTheme: IconThemeData(color: scheme.onSurfaceVariant),
        selectedLabelTextStyle: TextStyle(
          color: scheme.primary,
          fontWeight: FontWeight.w700,
        ),
        unselectedLabelTextStyle: TextStyle(color: scheme.onSurfaceVariant),
      ),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: scheme.surfaceContainer,
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(16),
          borderSide: BorderSide.none,
        ),
        enabledBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(16),
          borderSide: BorderSide.none,
        ),
        focusedBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(16),
          borderSide: BorderSide(color: scheme.primary, width: 1.5),
        ),
        contentPadding: const EdgeInsets.symmetric(
          horizontal: 18,
          vertical: 16,
        ),
        hintStyle: TextStyle(color: scheme.onSurfaceVariant),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: FilledButton.styleFrom(
          minimumSize: const Size(48, 48),
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(16),
          ),
        ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: OutlinedButton.styleFrom(
          minimumSize: const Size(48, 48),
          side: BorderSide(color: scheme.outlineVariant),
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(16),
          ),
        ),
      ),
      chipTheme: ChipThemeData(
        backgroundColor: scheme.surfaceContainer,
        selectedColor: scheme.primary.withValues(alpha: .12),
        side: BorderSide.none,
        shape: const StadiumBorder(),
        labelStyle: TextStyle(color: scheme.onSurface, fontSize: 13),
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
      ),
      listTileTheme: ListTileThemeData(
        iconColor: scheme.onSurfaceVariant,
        contentPadding: const EdgeInsets.symmetric(horizontal: 18, vertical: 4),
      ),
      popupMenuTheme: PopupMenuThemeData(
        color: scheme.surfaceContainerLow,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
      ),
      bottomSheetTheme: BottomSheetThemeData(
        backgroundColor: scheme.surfaceContainerLow,
        surfaceTintColor: Colors.transparent,
        showDragHandle: true,
        shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(26)),
        ),
      ),
    );
  }
}
