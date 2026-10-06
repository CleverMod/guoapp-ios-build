import base64
import json
import os
from pathlib import Path
from dataclasses import dataclass


@dataclass(frozen=True)
class BuildVariant:
    all_sources: bool = False

    @property
    def name(self):
        return '真果鉴' if self.all_sources else '红果鉴'

    @property
    def slug(self):
        return 'zhenguojian' if self.all_sources else 'hongguojian'

    @property
    def arguments(self):
        return ['--all-sources'] if self.all_sources else []

    @property
    def flutter_arguments(self):
        return ['--dart-define=ALL_SOURCES=' + str(self.all_sources).lower()]

    @property
    def linker_flags(self):
        return '-s -w -X duanjuapp/native/core.buildAllSources=' + str(self.all_sources).lower()

    @classmethod
    def from_dart_defines(cls, encoded):
        values = {}
        for item in encoded.split(','):
            if not item:
                continue
            key, separator, value = base64.b64decode(item, validate=True).decode('utf-8').partition('=')
            if separator:
                values[key] = value
        return cls(values.get('ALL_SOURCES') == 'true')


def add_variant_argument(parser):
    parser.add_argument('--all-sources', action='store_true',
                        help='构建包含全部站源的真果鉴；默认构建仅红果的红果鉴')


def source_access_flags(path=None, required=False):
    local = Path(__file__).resolve().parents[1] / 'native' / 'private' / 'source_access.json'
    raw = path.expanduser().read_bytes() if path else os.environ.get('GUOAPP_SOURCE_ACCESS', '').encode('utf-8')
    if not raw and local.is_file():
        raw = local.read_bytes()
    if not raw:
        if required:
            raise SystemExit('缺少内置站源授权，请提供 --source-access 或 GUOAPP_SOURCE_ACCESS。')
        return ''
    if len(raw) > 256 * 1024:
        raise SystemExit('站源授权配置超过 256 KiB。')
    try:
        config = json.loads(raw)
        if not isinstance(config, dict) or not config:
            raise ValueError()
        for source, access in config.items():
            if not isinstance(source, str) or not isinstance(access, dict):
                raise ValueError()
            if set(access) - {'headers', 'query', 'settings', 'privateKey', 'signKey', 'deviceId'}:
                raise ValueError()
            for key in ('headers', 'query', 'settings'):
                values = access.get(key, {})
                if not isinstance(values, dict) or any(not isinstance(k, str) or not isinstance(v, str) for k, v in values.items()):
                    raise ValueError()
                if key == 'headers' and any('\r' in k + v or '\n' in k + v for k, v in values.items()):
                    raise ValueError()
            if any(not isinstance(access.get(key, ''), str) for key in ('privateKey', 'signKey', 'deviceId')):
                raise ValueError()
    except (ValueError, TypeError):
        raise SystemExit('站源授权配置格式无效；实际内容已隐藏。') from None
    if required:
        web = config.get('ysp_live', {}).get('settings', {})
        names = ('appID', 'videoAppID', 'videoSecret', 'authSalt', 'liveSalt',
                 'cKeyKey', 'cKeyIV', 'cKeyMarker', 'version', 'cookie')
        if any(not web.get(name) for name in names):
            raise SystemExit('缺少央视频 Web 备用线路的内置签名配置，请更新 GUOAPP_SOURCE_ACCESS。')
        requirements = {
            'xiaopingguo': ('PUB1', 'NATIVE', 'DATAIV', 'DATAKEY', 'RR_SS', 'RR_DK', 'RR_IV', 'RR_API', 'RR_REF', 'RR_UA'),
            'luoxue': ('bfqPlayer', 'bfqReferer'),
            'jumi': ('discoveryURL', 'numberSeed', 'numberSuffix', 'appID'),
            'nnvideo': ('discoveryKey', 'discoveryURLs', 'hosts', 'xcConfig', 'zhenxiangURL', 'sjURL', 'backends', 'playerAliases'),
        }
        for source, names in requirements.items():
            settings = config.get(source, {}).get('settings', {})
            if any(not settings.get(name) for name in names):
                raise SystemExit(f'缺少 {source} 的内置协议配置，请更新 GUOAPP_SOURCE_ACCESS。')
        for source in (*requirements, 'xiaobao'):
            if not config.get(source, {}).get('headers', {}).get('User-Agent'):
                raise SystemExit(f'缺少 {source} 的内置请求头，请更新 GUOAPP_SOURCE_ACCESS。')
    encoded = base64.b64encode(json.dumps(config, ensure_ascii=False, separators=(',', ':')).encode('utf-8')).decode('ascii')
    return ' -X duanjuapp/native/core.bundledAttachedAccessBase64=' + encoded
