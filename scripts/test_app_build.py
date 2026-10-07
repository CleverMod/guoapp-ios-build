import base64
import json
import os
from pathlib import Path
import plistlib
import runpy
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from app_build import BuildVariant, source_access_flags
from configure_ios_branding import configure


def dart_defines(*values):
    return ','.join(base64.b64encode(value.encode()).decode() for value in values)


class AppBuildTests(unittest.TestCase):
    def test_required_source_access_rejects_stale_live_signing_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'source_access.json'
            for config in ({'other': {'headers': {'Token': 'fixture'}}},
                           {'ysp_live': {'settings': {'appID': 'fixture'}}}):
                path.write_text(json.dumps(config), encoding='utf-8')
                with self.assertRaisesRegex(SystemExit, 'GUOAPP_SOURCE_ACCESS'):
                    source_access_flags(path, required=True)

    def test_required_live_signing_inputs_are_bundled_with_other_sources(self):
        names = ('appID', 'videoAppID', 'videoSecret', 'authSalt', 'liveSalt',
                 'cKeyKey', 'cKeyIV', 'cKeyMarker', 'version', 'cookie')
        config = {'other': {'headers': {'Token': 'fixture'}},
                  'ysp_live': {'settings': {name: 'fixture' for name in names}}}
        requirements = {'xiaopingguo': ('PUB1', 'NATIVE', 'DATAIV', 'DATAKEY', 'RR_SS', 'RR_DK', 'RR_IV', 'RR_API', 'RR_REF', 'RR_UA'), 'luoxue': ('bfqPlayer', 'bfqReferer'), 'jumi': ('discoveryURL', 'numberSeed', 'numberSuffix', 'appID'), 'nnvideo': ('discoveryKey', 'discoveryURLs', 'hosts', 'xcConfig', 'zhenxiangURL', 'sjURL', 'backends', 'playerAliases')}
        for source, fields in requirements.items():
            config[source] = {'headers': {'User-Agent': 'fixture'}, 'settings': {name: 'fixture' for name in fields}}
        config['xiaobao'] = {'headers': {'User-Agent': 'fixture'}}
        manifest = Path(__file__).resolve().parents[1] / 'native/core/catpaw_sources.json'
        for entry in json.loads(manifest.read_text(encoding='utf-8')):
            if not entry['existing']:
                config[entry['id']] = {'settings': {
                    'script': entry['script'], 'scriptSHA256': entry['sha256'],
                    'extend': '', 'constants': '{}', 'defaults': '{}',
                }}
        config['catpaw_playback'] = {'settings': {'parses': '[{"url":"https://parser.test/?url="}]'}}
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'source_access.json'
            path.write_text(json.dumps(config), encoding='utf-8')
            flags = source_access_flags(path, required=True)
            encoded = flags.split('bundledAttachedAccessBase64=', 1)[1]
            self.assertEqual(json.loads(base64.b64decode(encoded)), config)

    def test_omitted_or_false_flag_keeps_hongguo_only(self):
        for encoded in ['', dart_defines('ALL_SOURCES=false'), dart_defines('OTHER=true')]:
            variant = BuildVariant.from_dart_defines(encoded)
            self.assertFalse(variant.all_sources)
            self.assertEqual(variant.name, '红果鉴')
            self.assertEqual(variant.slug, 'hongguojian')

    def test_full_edition_decodes_among_other_flutter_defines(self):
        variant = BuildVariant.from_dart_defines(dart_defines(
            'OTHER=中文', 'ALL_SOURCES=true', 'VALUE=a=b'))
        self.assertTrue(variant.all_sources)
        self.assertEqual(variant.name, '真果鉴')
        self.assertEqual(variant.slug, 'zhenguojian')

    def test_ios_branding_can_switch_editions_without_replacing_bundle_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'Info.plist'
            original = {'CFBundleIdentifier': 'com.duanju.duanjuApp', 'CFBundleVersion': '9'}
            path.write_bytes(plistlib.dumps(original))
            for enabled in [True, False]:
                configure(path, dart_defines('ALL_SOURCES=' + str(enabled).lower()))
                actual = plistlib.loads(path.read_bytes())
                self.assertEqual(actual['CFBundleDisplayName'], '真果鉴' if enabled else '红果鉴')
                self.assertEqual(actual['CFBundleName'], 'zhenguojian' if enabled else 'hongguojian')
                for key, value in original.items():
                    self.assertEqual(actual[key], value)

    @unittest.skipUnless(shutil.which('cmake'), 'CMake is unavailable')
    def test_windows_reads_defines_from_flutter_tool_environment(self):
        branding = Path(__file__).resolve().parents[1] / 'windows/runner/app_branding.cmake'
        for flags, expected in [
            ([], '红果鉴'),
            (['ALL_SOURCES=true'], '真果鉴'),
            (['OTHER=true', 'ALL_SOURCES=false'], '红果鉴'),
            (['ALL_SOURCES=true', 'ALL_SOURCES=false'], '红果鉴'),
        ]:
            with self.subTest(flags=flags), tempfile.TemporaryDirectory() as temporary:
                script = Path(temporary) / 'check.cmake'
                result = Path(temporary) / 'name.txt'
                script.write_text(
                    'list(APPEND FLUTTER_TOOL_ENVIRONMENT "OTHER=1" "DART_DEFINES=' + dart_defines(*flags) + '")\n'
                    'include("' + branding.as_posix() + '")\n'
                    'file(WRITE "' + result.as_posix() + '" "${APP_DISPLAY_NAME}")\n',
                    encoding='utf-8',
                )
                subprocess.run(['cmake', '-P', str(script)], check=True, capture_output=True)
                self.assertEqual(result.read_text(encoding='utf-8'), expected)

    def test_android_and_windows_propagate_one_edition_to_core_flutter_and_package(self):
        root = Path(__file__).resolve().parent
        for target in ['android', 'windows']:
            for enabled in [False, True]:
                with self.subTest(target=target, all_sources=enabled):
                    script = root / f'build_{target}.py'
                    arguments = [str(script)] + (['--all-sources'] if enabled else [])
                    with mock.patch.object(sys, 'argv', arguments), \
                            mock.patch.dict(os.environ, {'PATH': '/tools'}, clear=True), \
                            mock.patch('shutil.which', return_value='/tools/flutter'), \
                            mock.patch('platform.system', return_value='Windows'), \
                            mock.patch('subprocess.run') as run:
                        runpy.run_path(str(script), run_name='__main__')
                    calls = [call.args[0] for call in run.call_args_list]
                    native = next(call for call in calls if any(str(arg).endswith('build_native.py') for arg in call))
                    flutter = next(call for call in calls if 'build' in call)
                    package = next(call for call in calls if any(str(arg).endswith('package_release.py') for arg in call))
                    self.assertEqual('--all-sources' in native, enabled)
                    self.assertEqual('--all-sources' in package, enabled)
                    self.assertIn('--dart-define=ALL_SOURCES=' + str(enabled).lower(), flutter)
                    self.assertIn('core.buildAllSources=' + str(enabled).lower(), BuildVariant(enabled).linker_flags)


if __name__ == '__main__':
    unittest.main()
