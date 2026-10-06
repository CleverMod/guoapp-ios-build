import argparse
import base64
import re
import zipfile
from pathlib import Path


def read_uint(data, offset):
    value = shift = 0
    while True:
        byte = data[offset]
        offset += 1
        value |= (byte & 127) << shift
        if byte < 128:
            return value, offset
        shift += 7


def write_uint(value):
    result = bytearray()
    while value >= 128:
        result.append((value & 127) | 128)
        value >>= 7
    result.append(value)
    return bytes(result)


def read_name(data, offset):
    length, offset = read_uint(data, offset)
    return data[offset:offset + length].decode('utf-8'), offset + length


def read_limits(data, offset):
    flags, offset = read_uint(data, offset)
    _, offset = read_uint(data, offset)
    if flags & 1:
        _, offset = read_uint(data, offset)
    return offset


def ticket_with_local_memory(data):
    if data[:8] != b'\x00asm\x01\x00\x00\x00':
        raise ValueError('Invalid ticket WebAssembly')
    sections = {}
    offset = 8
    while offset < len(data):
        kind = data[offset]
        size, start = read_uint(data, offset + 1)
        if kind:
            sections[kind] = data[start:start + size]
        offset = start + size
    imports = sections[2]
    count, offset = read_uint(imports, 0)
    functions = []
    table = memory = None
    for _ in range(count):
        start = offset
        module, offset = read_name(imports, offset)
        name, offset = read_name(imports, offset)
        kind = imports[offset]
        offset += 1
        type_start = offset
        if kind == 0:
            _, offset = read_uint(imports, offset)
            functions.append(imports[start:offset])
        elif kind == 1 and module == 'a' and name == 'b':
            offset = read_limits(imports, offset + 1)
            table = imports[type_start:offset]
        elif kind == 2 and module == 'a' and name == 'a':
            offset = read_limits(imports, offset)
            memory = imports[type_start:offset]
        else:
            raise ValueError('Unexpected ticket import')
    if table is None or memory is None or 4 in sections or 5 in sections:
        raise ValueError('Unexpected ticket memory or table')
    sections[2] = write_uint(len(functions)) + b''.join(functions)
    sections[4] = b'\x01' + table
    sections[5] = b'\x01' + memory
    exports = sections[7]
    count, start = read_uint(exports, 0)
    sections[7] = write_uint(count + 1) + exports[start:] + b'\x06memory\x02\x00'
    result = bytearray(data[:8])
    for kind in (1, 2, 3, 4, 5, 6, 7, 8, 9, 12, 10, 11):
        if kind in sections:
            payload = sections[kind]
            result.extend(bytes([kind]) + write_uint(len(payload)) + payload)
    return bytes(result)


def main():
    parser = argparse.ArgumentParser(description='Extract ysp-live Web signing modules for the native core')
    parser.add_argument('archive', type=Path)
    options = parser.parse_args()
    with zipfile.ZipFile(options.archive) as archive:
        names = [name for name in archive.namelist() if name.endswith('/ysp-engine.js')]
        if len(names) != 1:
            raise ValueError('Expected one ysp-engine.js')
        source = archive.read(names[0]).decode('utf-8')
    destination = Path(__file__).resolve().parents[1] / 'native' / 'core' / 'ysp_web'
    destination.mkdir(exist_ok=True)
    for symbol, filename in (('KEYGEN_B64', 'keygen.wasm'), ('TICKET_B64', 'ticket.wasm')):
        match = re.search(r"const " + symbol + r" = '([A-Za-z0-9+/=]+)';", source)
        if match is None:
            raise ValueError('Missing WebAssembly module: ' + symbol)
        data = base64.b64decode(match.group(1), validate=True)
        if symbol == 'TICKET_B64':
            data = ticket_with_local_memory(data)
        (destination / filename).write_bytes(data)
        print(f'{filename}: {len(data)} bytes')


if __name__ == '__main__':
    main()
