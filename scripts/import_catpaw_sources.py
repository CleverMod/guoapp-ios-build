import argparse
import ast
import hashlib
import json
import re
import urllib.request
from pathlib import Path


SOURCE_IDS = (
    'catpaw_bajie', 'catpaw_proxy_drama', 'catpaw_xinlang', 'catpaw_duboku',
    'catpaw_yiys', 'jumi', 'catpaw_ciyuancheng', 'catpaw_xifandongman',
    'catpaw_meijuxia', 'catpaw_rebo', 'catpaw_xinxin', 'catpaw_gulu',
    'catpaw_zhuifan', 'catpaw_quanyingshi', 'catpaw_fanxi', 'catpaw_bidi',
    'catpaw_bilfun', 'catpaw_silisili', 'catpaw_jikan', 'catpaw_kankelm',
    'catpaw_2kdm', 'catpaw_maitian', 'catpaw_xingma', 'catpaw_gotto',
    'catpaw_gugu', 'catpaw_jiuxiao', 'catpaw_yiyi', 'catpaw_luogongge',
    'catpaw_kaiduan', 'catpaw_247', 'catpaw_proxy_artjx',
    'catpaw_proxy_ryjx', 'catpaw_proxy_fyjx',
)
SOURCE_SCRIPTS = (
    '八戒影视.py', 'proxy_drama.py', '新浪资源.py', '独播库.py', '壹影视.py',
    '看客TV.py', 'cycapp.py', 'cycapp.py', '美剧侠.py', 'RJAPP.py', 'XinJie.py',
    'Appfox.py', 'Appfox.py', 'FeiApp.py', 'ApptoV5无加密.py', 'AppMuou.py',
    'AppMuou.py', 'skapp.py', 'skapp.py', 'skapp.py', 'skapp.py', 'AppV2.py',
    'AppV2.py', 'AppV2.py', 'getapp3.4.6.py', 'Hmys.py', '99APP2.py',
    '99APP2.py', '开端.py', '247看.py', 'proxy_artjx.py', 'proxy_ryjx.py',
    'proxy_fyjx.py',
)


def script_configuration(code):
    tree = ast.parse(code)
    defaults = {}
    ordered = []

    class Collect(ast.NodeVisitor):
        def visit_Constant(self, node):
            if isinstance(node.value, str):
                value = node.value
                if (('PRIVATE KEY' in value or 'PUBLIC KEY' in value)
                        or (re.fullmatch(r'[A-Za-z0-9+/=_-]{16,}', value) and not value.startswith('/'))
                        or (len(value) > 180 and '\\' not in value
                            and not value.startswith(('http:', 'https:')))):
                    ordered.append(value)

    Collect().visit(tree)
    constants = {'const' + str(i + 1): value for i, value in enumerate(ordered)}
    for node in ast.walk(tree):
        if not isinstance(node, ast.Assign):
            continue
        for target in node.targets:
            targets = target.elts if isinstance(target, (ast.Tuple, ast.List)) else [target]
            values = node.value.elts if isinstance(node.value, (ast.Tuple, ast.List)) else [node.value]
            for name, value in zip(targets, values):
                field = name.id if isinstance(name, ast.Name) else name.attr if isinstance(name, ast.Attribute) else ''
                try:
                    literal = ast.literal_eval(value)
                except (ValueError, TypeError):
                    continue
                try:
                    json.dumps(literal)
                except TypeError:
                    continue
                if field and literal and field not in defaults:
                    defaults[field] = literal
    for node in ast.walk(tree):
        if isinstance(node, ast.Constant) and isinstance(node.value, str):
            if node.value.startswith('iv_salt_'):
                constants['ryIVSalt'] = node.value
        if isinstance(node, ast.FunctionDef) and node.name in ('des3', 'yd_xor'):
            for child in ast.walk(node):
                if isinstance(child, ast.Assign):
                    for target in child.targets:
                        if isinstance(target, ast.Name):
                            value = child.value
                            if isinstance(value, ast.Call) and isinstance(value.func, ast.Attribute):
                                value = value.func.value
                            if isinstance(value, ast.Constant) and isinstance(value.value, str):
                                constants[node.name + '_' + target.id] = value.value
    return constants, defaults


def import_sources(player, project, script_directory=None):
    settings = json.loads((player / 'catpaw_data/settings.json').read_text(encoding='utf-8-sig'))
    subscriptions = [s for s in settings.get('pythonSubscriptions', []) if len(s.get('sources', [])) == 33]
    if len(subscriptions) != 1:
        raise ValueError('未找到唯一的 33 源订阅配置。')
    subscription = subscriptions[0]
    access_path = project / 'native/private/source_access.json'
    access = json.loads(access_path.read_text(encoding='utf-8'))
    definitions, loaded = [], {}
    for source_id, filename, source in zip(SOURCE_IDS, SOURCE_SCRIPTS, subscription['sources']):
        if Path(source['originalApi']).name != filename or not source.get('enabled'):
            raise ValueError('源顺序或启用状态与已适配配置不符。')
        if filename not in loaded:
            cache = player / 'catpaw_data/python_sources' / source['fileName']
            extracted = script_directory / filename if script_directory else None
            if extracted and extracted.is_file():
                code = extracted.read_text(encoding='utf-8-sig')
            elif cache.is_file():
                code = cache.read_text(encoding='utf-8-sig')
            else:
                request = urllib.request.Request(source['remoteUrl'], headers={'User-Agent': 'Mozilla/5.0'})
                with urllib.request.urlopen(request, timeout=30) as response:
                    code = response.read(3 << 20).decode('utf-8-sig')
            constants, defaults = script_configuration(code)
            loaded[filename] = (constants, defaults, hashlib.sha256(code.encode('utf-8')).hexdigest())
        constants, defaults, checksum = loaded[filename]
        name = source['displayName'].split('[', 1)[0]
        definition = {'id': source_id, 'name': name, 'script': filename, 'sha256': checksum,
                      'helper': source_id.startswith('catpaw_proxy_'), 'existing': source_id == 'jumi'}
        definitions.append(definition)
        if source_id == 'jumi':
            ext = json.loads(source['extend'])
            access[source_id]['settings']['discoveryURL'] = ext['CosUrl']
            access[source_id]['settings']['catpawScriptSHA256'] = checksum
            continue
        headers = next((defaults[k] for k in ('headers', 'def_headers2')
                        if isinstance(defaults.get(k), dict)), {})
        headers = {k: str(v) for k, v in headers.items() if isinstance(v, (str, int))}
        headers.pop('Accept-Encoding', None)
        headers.pop('Connection', None)
        access[source_id] = {'headers': headers, 'settings': {
            'script': filename, 'scriptSHA256': checksum, 'extend': source['extend'],
            'constants': json.dumps(constants, ensure_ascii=False, separators=(',', ':')),
            'defaults': json.dumps(defaults, ensure_ascii=False, separators=(',', ':')),
        }}
        if source_id == 'catpaw_bajie':
            access[source_id]['deviceId'] = constants['const1']
    access['catpaw_playback'] = {'settings': {
        'parses': json.dumps(subscription['playback'].get('parses', []), ensure_ascii=False, separators=(',', ':')),
        'rules': json.dumps(subscription['playback'].get('rules', []), ensure_ascii=False, separators=(',', ':')),
        'ads': json.dumps(subscription['playback'].get('ads', []), ensure_ascii=False, separators=(',', ':')),
        'flags': json.dumps(subscription['playback'].get('flags', []), ensure_ascii=False, separators=(',', ':')),
    }}
    raw = json.dumps(access, ensure_ascii=False, indent=2).encode('utf-8')
    if len(raw) > 256 << 10:
        raise ValueError('迁移后的私有配置超出构建输入大小限制。')
    access_path.write_bytes(raw + b'\n')
    manifest = project / 'native/core/catpaw_sources.json'
    manifest.write_text(json.dumps(definitions, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    return len(definitions), len(loaded), len(raw)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('player', type=Path)
    parser.add_argument('--scripts', type=Path)
    options = parser.parse_args()
    project = Path(__file__).resolve().parents[1]
    entries, scripts, size = import_sources(options.player, project, options.scripts)
    print(f'已提取 {entries} 个配置、{scripts} 份脚本；私有构建输入 {size} 字节，凭据已隐藏。')


if __name__ == '__main__':
    main()
