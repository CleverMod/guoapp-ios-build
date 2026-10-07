import 'package:flutter/material.dart';

import 'app_surface.dart';

class AppBottomNavigation extends StatelessWidget {
  const AppBottomNavigation({
    super.key,
    required this.selectedIndex,
    required this.onDestinationSelected,
    required this.destinations,
  });

  final int selectedIndex;
  final ValueChanged<int> onDestinationSelected;
  final List<NavigationDestination> destinations;

  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    return SafeArea(
      top: false,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 8, 16, 10),
        child: AppSurface(
          radius: 28,
          elevated: true,
          padding: const EdgeInsets.all(6),
          child: Row(
            children: [
              for (final (index, destination) in destinations.indexed)
                Expanded(
                  child: Semantics(
                    container: true,
                    button: true,
                    selected: index == selectedIndex,
                    child: Tooltip(
                      message: destination.label,
                      excludeFromSemantics: true,
                      child: AnimatedContainer(
                        duration: MediaQuery.disableAnimationsOf(context)
                            ? Duration.zero
                            : const Duration(milliseconds: 180),
                        curve: Curves.easeOutCubic,
                        decoration: BoxDecoration(
                          color: index == selectedIndex
                              ? colors.primary.withValues(alpha: .12)
                              : Colors.transparent,
                          borderRadius: BorderRadius.circular(22),
                        ),
                        child: InkWell(
                          key: ValueKey('bottom-nav-$index'),
                          onTap: () => onDestinationSelected(index),
                          borderRadius: BorderRadius.circular(22),
                          child: ConstrainedBox(
                            constraints: const BoxConstraints(
                              minHeight: 58,
                              minWidth: 48,
                            ),
                            child: Padding(
                              padding: const EdgeInsets.symmetric(
                                horizontal: 2,
                                vertical: 8,
                              ),
                              child: Column(
                                mainAxisSize: MainAxisSize.min,
                                children: [
                                  IconTheme(
                                    data: IconThemeData(
                                      size: 24,
                                      color: index == selectedIndex
                                          ? colors.primary
                                          : colors.onSurfaceVariant,
                                    ),
                                    child: index == selectedIndex
                                        ? destination.selectedIcon ??
                                              destination.icon
                                        : destination.icon,
                                  ),
                                  const SizedBox(height: 5),
                                  Text(
                                    destination.label,
                                    textAlign: TextAlign.center,
                                    maxLines: 2,
                                    overflow: TextOverflow.ellipsis,
                                    style: TextStyle(
                                      fontSize: 11,
                                      height: 1.2,
                                      fontWeight: index == selectedIndex
                                          ? FontWeight.w700
                                          : FontWeight.w500,
                                      color: index == selectedIndex
                                          ? colors.primary
                                          : colors.onSurfaceVariant,
                                    ),
                                  ),
                                ],
                              ),
                            ),
                          ),
                        ),
                      ),
                    ),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}
