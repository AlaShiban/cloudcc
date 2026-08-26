"""Where the SDK supplies a client, that client and the injected
_cloudcc_runtime one are two implementations of a single API, and they will
drift unless something compares them.

This test asserts, method by method, that every public method on an SDK class
exists on its runtime counterpart with the same signature. It is what makes IDE
autocomplete against the SDK honest about what the compiled program will do.

Most capabilities are not in this list, and that is the point of the design
rather than a gap in the test. A program that hands ``persist`` a
``redis.Redis``, a SQLAlchemy engine, a ``pathlib.Path`` or a boto3 ``Table``
gets back an object of that same type once compiled -- a real Redis client, a
real Engine, a cloudpathlib S3Path, a real Table -- so there is no second
implementation to keep in step.

The list shrank by one when the key/value store stopped being a class this SDK
supplies. That is the direction of travel: every pair removed from here is a
pair that can no longer drift.
"""

import inspect
import pathlib
import sys

import pytest

import cloudcompiler as cloudcc
from cloudcompiler import _emulation

# The runtime shims live in the compiler's template tree; the SDK package does
# not depend on them, so they are loaded by path.
SHIM_DIR = (
    pathlib.Path(__file__).resolve().parents[3]
    / "internal"
    / "runtime"
    / "py"
    / "templates"
    / "_cloudcc_runtime"
)

#: emulation class -> (shim module, shim class). Both sides are named the same
#: on purpose; the mapping is explicit anyway so a rename cannot pass silently.
PAIRS = [
    (_emulation.Secret, "secret", "Secret"),
    (_emulation.Topic, "pubsub", "Topic"),
    (_emulation.Gateway, "expose", "Gateway"),
]

#: Shims that return the library's own type rather than a class of ours. There
#: is nothing to compare method-by-method; what matters is that they still
#: offer connect(), which the entrypoint test below checks.
TYPE_PRESERVING = ["fs", "kv", "orm", "redis_"]

#: Shims that stand in for something the SDK never supplied a class for. `rpc`
#: replaces another unit's *module*, so there is no pair to compare and no
#: library type to preserve -- but it is entered the same way as every other
#: shim, and that entrypoint is worth pinning.
STANDS_IN = ["rpc"]


def load_shim(module_name):
    """Parse a shim module without importing boto3."""
    import ast

    source = (SHIM_DIR / f"{module_name}.py").read_text()
    return ast.parse(source)


def shim_methods(tree, class_name):
    """Return {method name: [parameter names]} for a class in a parsed shim."""
    import ast

    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef) and node.name == class_name:
            out = {}
            for item in node.body:
                if isinstance(item, ast.FunctionDef) and not item.name.startswith("_"):
                    out[item.name] = [a.arg for a in item.args.args]
            return out
    raise AssertionError(f"class {class_name} not found in the shim")


def emulation_methods(cls):
    out = {}
    for name, member in inspect.getmembers(cls, predicate=inspect.isfunction):
        if name.startswith("_"):
            continue
        out[name] = list(inspect.signature(member).parameters)
    return out


@pytest.mark.parametrize("cls,module,shim_class", PAIRS, ids=[p[1] for p in PAIRS])
def test_public_api_matches(cls, module, shim_class):
    tree = load_shim(module)
    shim = shim_methods(tree, shim_class)
    local = emulation_methods(cls)

    missing = sorted(set(local) - set(shim))
    assert not missing, (
        f"{cls.__name__} offers {missing} but _cloudcc_runtime/{module}.py's "
        f"{shim_class} does not; a program that works locally would fail once "
        f"compiled"
    )

    extra = sorted(set(shim) - set(local))
    assert not extra, (
        f"_cloudcc_runtime/{module}.py's {shim_class} offers {extra} but the SDK "
        f"stub does not; the IDE would not suggest it"
    )

    for name in sorted(local):
        assert local[name] == shim[name], (
            f"{cls.__name__}.{name}{tuple(local[name])} does not match "
            f"{shim_class}.{name}{tuple(shim[name])}"
        )


@pytest.mark.parametrize(
    "module", sorted([p[1] for p in PAIRS] + TYPE_PRESERVING + STANDS_IN)
)
def test_every_shim_has_a_connect_entrypoint(module):
    import ast

    tree = load_shim(module)
    names = {
        node.name
        for node in tree.body
        if isinstance(node, ast.FunctionDef) and not node.name.startswith("_")
    }
    # expose and config are entered through differently-named functions.
    expected = "register" if module == "expose" else "connect"
    assert expected in names, f"_cloudcc_runtime/{module}.py has no {expected}()"


def test_the_sdk_surface_is_the_documented_one():
    """Every function named in __all__ exists and is callable."""
    for name in cloudcc.__all__:
        assert hasattr(cloudcc, name), f"{name} is exported but missing"


def test_no_shim_imports_boto3_outside_the_client_module():
    for path in sorted(SHIM_DIR.glob("*.py")):
        source = path.read_text()
        if path.name == "_client.py":
            assert "import boto3" in source
            continue
        assert "import boto3" not in source, (
            f"{path.name} imports boto3 directly; every client should go "
            f"through _client so the endpoint override applies uniformly"
        )
