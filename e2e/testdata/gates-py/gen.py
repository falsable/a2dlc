"""Genera y congela los casos E2E de los gates de tests (escalón 1). Se corre UNA vez, antes de escribir los gates;
casos.json guarda el sha256 de cada archivo y medir_gates.sh se niega a correr si no cuadra.
Uso: python e2e/testdata/gates-py/gen.py

Cada caso es la base común (base/) más un overlay del candidato en el árbol de trabajo, con el veredicto y los
tests que se esperan. Lo que no se puede volver determinista con pytest real (flaky en la base, rojo intermitente
solo en el candidato) se prueba con el runner falso en internal/app."""
import hashlib
import json
import sys
from pathlib import Path

D = Path(__file__).resolve().parent
if (D / "casos.json").exists():
    sys.exit("ya congelados: no se regeneran")

BASE = {
    "src/__init__.py": "",
    "src/calc.py": '''def suma(a: int, b: int) -> int:
    return a + b


def redondea(x: float) -> int:
    return int(x + 0.5)


def divide(a: int, b: int) -> float:
    return a / b


class Externo:
    class Interno:
        def viejo(self) -> int:
            return 0
''',
    "tests/__init__.py": "",
    "tests/test_calc.py": '''from src.calc import redondea, suma


def test_suma():
    assert suma(2, 3) == 5


def test_redondea():
    assert redondea(2.4) == 2
''',
    # rojo en la base: «no empeora» no lo cuenta como nuevo
    "tests/test_roto.py": '''def test_roto_previo():
    assert False, "rojo desde antes del cambio"
''',
}

CALC = BASE["src/calc.py"]
MEDIA = CALC + '''

def media(xs: list[int]) -> float:
    return sum(xs) / len(xs)
'''

CASOS = [
    {"id": "C1-feature-con-test", "esperado": "PASS",
     "que": "función nueva con su test: en la base el stub lanza NotImplementedError, fallo válido",
     "overlay": {"src/calc.py": MEDIA, "tests/test_media.py": '''from src.calc import media


def test_media():
    assert media([1, 2, 3]) == 2
'''},
     "tests": [{"name": "test_media", "gate": "nop_oracle", "verdict": "PASS"}]},
    {"id": "C2-test-sin-dientes", "esperado": "FAIL",
     "que": "pytest.raises(Exception) también atrapa el NotImplementedError del stub: el test no prueba el cambio",
     "overlay": {"src/calc.py": MEDIA, "tests/test_media.py": '''import pytest

from src.calc import media


def test_media_vacia():
    with pytest.raises(Exception):
        media([])
'''},
     "tests": [{"name": "test_media_vacia", "gate": "nop_oracle", "verdict": "FAIL"}]},
    {"id": "C3-bugfix-con-waiver", "esperado": "PASS",
     "que": "bug fix declarado con waiver (pendiente de firma): el test nuevo falla en la base por el crash que arregla",
     "overlay": {"src/calc.py": CALC.replace("    return a / b\n", "    if b == 0:\n        return 0.0\n    return a / b\n"),
                 "tests/test_divide.py": '''from src.calc import divide


def test_divide_por_cero():
    assert divide(1, 0) == 0.0
''',
                 ".a2dlc/waivers.json": json.dumps({"schema_version": 1, "waivers": [
                     {"target": "src/calc.py::divide", "decision": "enforce_new_behavior",
                      "reason": "dividir por cero devuelve 0.0 en vez de romper"}]}) + "\n"},
     "tests": [{"name": "test_divide_por_cero", "gate": "nop_oracle", "verdict": "PASS"}],
     "waivers_pendientes": ["src/calc.py::divide"]},
    {"id": "C4-refactor-caracterizacion", "esperado": "PASS",
     "que": "refactor equivalente con un test de caracterización que pasa en la base: sin stubs ni waivers, no bloquea",
     "overlay": {"src/calc.py": CALC.replace("return int(x + 0.5)", "return int(0.5 + x)"),
                 "tests/test_refactor.py": '''from src.calc import redondea


def test_redondea_medio():
    assert redondea(2.5) == 3
'''},
     "tests": [{"name": "test_redondea_medio", "gate": "nop_oracle", "verdict": "PASS", "characterization": True}]},
    {"id": "C5-test-nuevo-rojo", "esperado": "FAIL",
     "que": "test nuevo que falla también en el candidato",
     "overlay": {"tests/test_mal.py": '''from src.calc import suma


def test_suma_mal():
    assert suma(1, 1) == 3
'''},
     "tests": [{"name": "test_suma_mal", "gate": "nop_oracle", "verdict": "FAIL"}]},
    {"id": "C6-regresion", "esperado": "FAIL",
     "que": "regresión de un test existente: verde en la base, rojo en el candidato (el motor también la ve)",
     "overlay": {"src/calc.py": CALC.replace("return a + b", "return a - b")},
     "tests": [{"name": "test_suma", "gate": "baseline", "verdict": "FAIL"}]},
    {"id": "C7-metodo-anidado-sin-stub", "esperado": "INCONCLUSIVE",
     "que": "método nuevo en una clase anidada: el motor no lo stubea y la base cae por AttributeError sobre él",
     "overlay": {"src/calc.py": CALC.replace("            return 0\n", "            return 0\n\n        def nuevo(self) -> int:\n            return 1\n"),
                 "tests/test_anidado.py": '''from src.calc import Externo


def test_nuevo():
    assert Externo.Interno().nuevo() == 1
'''},
     "tests": [{"name": "test_nuevo", "gate": "nop_oracle", "verdict": "INCONCLUSIVE"}]},
    {"id": "C8-corrida-sin-tests", "esperado": "INCONCLUSIVE",
     "que": "un conftest que mata el proceso: corrida roja sin tests identificables",
     "overlay": {"tests/conftest.py": "import os\n\nos._exit(3)\n"},
     "tests": []},
    {"id": "C9-test-modificado", "esperado": "PASS",
     "que": "archivo de tests modificado: los tests viejos no cuentan como nuevos; el nuevo pasa en la base (caracterización)",
     "overlay": {"tests/test_calc.py": BASE["tests/test_calc.py"] + '''

def test_suma_negativos():
    assert suma(-2, -3) == -5
'''},
     "tests": [{"name": "test_suma_negativos", "gate": "nop_oracle", "verdict": "PASS", "characterization": True}],
     "no_juzgados": ["test_suma", "test_redondea"]},
]


def sha(t):
    return hashlib.sha256(t.encode("utf-8")).hexdigest()


def escribe(raiz, archivos):
    out = {}
    for ruta, texto in archivos.items():
        p = raiz / ruta
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(texto.encode("utf-8"))
        out[ruta] = sha(texto)
    return out


manifiesto = {"base": escribe(D / "base", BASE), "casos": []}
for c in CASOS:
    archivos = escribe(D / "casos" / c["id"], c.pop("overlay"))
    manifiesto["casos"].append({**c, "archivos": archivos})
(D / "casos.json").write_text(json.dumps(manifiesto, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
print(len(CASOS), "casos en", D)
