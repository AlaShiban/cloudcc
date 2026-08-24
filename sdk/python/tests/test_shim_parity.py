"""The SDK stubs and the injected _cc_runtime clients are two implementations
of one API. They will drift unless something compares them.

This test imports both sides and asserts, method by method, that every public
method on an SDK emulation exists on its runtime counterpart with the same
signature. It is what makes IDE autocomplete against the SDK honest about what
the compiled program will actually do.
"""

import inspect
import pathlib
import sys

import pytest

import cloudcompiler as cc
from cloudcompiler import _emulation

# The runtime shims live in the compiler's template tree; the SDK package does
# not depend on them, so they are loaded by path.
SHIM_DIR = (
    pathlib.Path(__file__).resolve().parents[3]
    / "internal"
    / "runtime"
    / "py"
    / "templates"
    / "_cc_runtime"
)

#: emulation class -> (shim module, shim class). Both sides are named the same
#: on purpose; the mapping is explicit anyway so a rename cannot pass silently.
PAIRS = [
    (_emulation.KVStore, "kv", "KVStore"),
    (_emulation.Bucket, "fs", "Bucket"),
    (_emulation.Secret, "secret", "Secret"),
    (_emulation.OrmSession, "orm", "OrmSession"),
    (_emulation.Redis, "redis_", "Redis"),
    (_emulation.Topic, "pubsub", "Topic"),
    (_emulation.Gateway, "expose", "Gateway"),
]


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
        f"{cls.__name__} offers {missing} but _cc_runtime/{module}.py's "
        f"{shim_class} does not; a program that works locally would fail once "
        f"compiled"
    )

    extra = sorted(set(shim) - set(local))
    assert not extra, (
        f"_cc_runtime/{module}.py's {shim_class} offers {extra} but the SDK "
        f"stub does not; the IDE would not suggest it"
    )

    for name in sorted(local):
        assert local[name] == shim[name], (
            f"{cls.__name__}.{name}{tuple(local[name])} does not match "
            f"{shim_class}.{name}{tuple(shim[name])}"
        )


@pytest.mark.parametrize("module", sorted(p[1] for p in PAIRS))
def test_every_shim_has_a_connect_entrypoint(module):
    import ast

    tree = load_shim(module)
    names = {
        node.name
        for node in tree.body
        if isinstance(node, ast.FunctionDef) and not node.name.startswith("_")
    }
    # expose and config are entered through differently-named functions.
    expected = {"kv": "connect", "fs": "connect", "secret": "connect",
                "orm": "connect", "redis_": "connect", "pubsub": "connect",
                "expose": "register"}[module]
    assert expected in names, f"_cc_runtime/{module}.py has no {expected}()"


def test_the_sdk_surface_is_the_documented_one():
    """Every function named in __all__ exists and is callable."""
    for name in cc.__all__:
        assert hasattr(cc, name), f"{name} is exported but missing"


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
