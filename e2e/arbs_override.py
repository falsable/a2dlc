"""Converts the generators calibrated by hand in the pilot (arbs.ts, frozen sha256) to a2dlc's override format:
`export default (fc) => ({ "<function>": fc.tuple(...) })`, without importing fast-check. It does not change a single generator.
Usage: python arbs_override.py <pilot arbs.ts> > .a2dlc/arbs.ts"""
import re
import sys
from pathlib import Path

src = Path(sys.argv[1]).read_text(encoding="utf-8")
src = src.replace("import fc from 'fast-check';", "export default (fc: any) => {")
src = re.sub(r"export interface Objetivo \{.*?\n\}\n", "", src, flags=re.S)
src = src.replace("export const OBJETIVOS: Record<string, Objetivo> =", "const OBJETIVOS: Record<string, any> =")
src += "\nreturn Object.fromEntries(Object.values(OBJETIVOS).map((o: any) => [o.exportado, o.args]));\n};\n"
sys.stdout.write(src)
