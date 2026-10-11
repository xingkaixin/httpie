"""Export the baseline's built-in ANSI palettes; no Python is needed at runtime."""
import json
from pathlib import Path
from pygments.console import ansiformat
from pygments.formatters import Terminal256Formatter
from pygments.formatters.terminal import TERMINAL_COLORS
from pygments.styles import get_all_styles
from httpie.output.formatters.colors import PIE_STYLES, Solarized256Style

result = {}
for name in sorted(get_all_styles()):
    formatter = Terminal256Formatter(style=name)
    result[name] = {key.removeprefix('Token.').replace('.', ''): list(value)
                    for key, value in formatter.style_string.items()}
for name, (headers, body) in PIE_STYLES.items():
    for suffix, style in [('', body), ('-headers', headers)]:
        formatter = Terminal256Formatter(style=style)
        result[name + suffix] = {key.removeprefix('Token.').replace('.', ''): list(value)
                               for key, value in formatter.style_string.items()}
result['auto'] = {}
formatter = Terminal256Formatter(style=Solarized256Style)
result['solarized'] = {key.removeprefix('Token.').replace('.', ''): list(value)
                       for key, value in formatter.style_string.items()}
for token, colors in TERMINAL_COLORS.items():
    formatted = ansiformat(colors[0], 'VALUE') if colors[0] else 'VALUE'
    result['auto'][str(token).removeprefix('Token.').replace('.', '')] = formatted.split('VALUE')
Path('internal/cli/palettes.json').write_text(json.dumps(result, sort_keys=True, separators=(',', ':')) + '\n')
