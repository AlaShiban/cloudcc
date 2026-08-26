"""Every method an example calls on a persisted client must exist on it.

`persist` hands back the library's own client, and the whole argument for that
design is in stores.py's own docstring: "the compiled program can use every
method those libraries have rather than the handful someone thought to wrap".

The failure mode that creates is the mirror image of the old one. There is no
parallel API to drift, but an example can call a method the real client does
not have, and nothing says so until that line runs -- which for a subscriber or
an error path may be nowhere in any test. Four such calls survived in this
repository for exactly that reason: `audit.write(...)`, `docs.write(...)` twice
and `docs.list()`, all left over from when the SDK supplied a FileStore class
with those methods. Three of them were in an example that cannot be deployed to
the emulator, and the fourth was in a handler nothing invoked.

The check asks Python what the type has rather than listing it here. A table of
"methods pathlib.Path supports" maintained in this file would be a parallel API
again, one test removed.

Only types whose clients construct offline are checked; a boto3 Table or a
Redis client is not one of them, and pretending otherwise would mean this suite
needing an emulator to run.
"""

import ast
import pathlib

import pytest

EXAMPLES = pathlib.Path(__file__).resolve().parents[3] / "examples"

#: Constructor name -> the type persist gives back for it. The compiled program
#: gets a different class (a cloudpathlib S3Path for a Path), but it is chosen
#: to mirror this one, and it is this one that the uncompiled program holds.
CHECKED = {"Path": pathlib.Path}


def example_files():
    if not EXAMPLES.is_dir():
        return []
    return sorted(EXAMPLES.rglob("*.py"))


def constructor_of(node):
    """The name at the head of a client expression: Path("x") -> "Path"."""
    if not isinstance(node, ast.Call):
        return None
    func = node.func
    if isinstance(func, ast.Name):
        return func.id
    if isinstance(func, ast.Attribute):
        return func.attr
    return None


def persisted_handles(tree):
    """Map a variable name to the type persist returned for it.

    Matches `name = cloudcc.persist(Path(...), id=...)`, which is the shape the
    compiler itself reads. Anything more clever than a module-level assignment
    is out of scope here for the same reason it is out of scope there.
    """
    handles = {}
    for node in ast.walk(tree):
        if not isinstance(node, ast.Assign) or len(node.targets) != 1:
            continue
        target = node.targets[0]
        if not isinstance(target, ast.Name):
            continue
        call = node.value
        if constructor_of(call) != "persist" or not call.args:
            continue
        checked = CHECKED.get(constructor_of(call.args[0]))
        if checked is not None:
            handles[target.id] = checked
    return handles


def attribute_uses(tree, names):
    """Every `handle.attr` in the file, as (handle, attr, line)."""
    out = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Attribute) and isinstance(node.value, ast.Name):
            if node.value.id in names:
                out.append((node.value.id, node.attr, node.lineno))
    return out


@pytest.mark.parametrize("path", example_files(), ids=lambda p: str(p.name))
def test_every_call_on_a_persisted_client_exists(path):
    if not EXAMPLES.is_dir():
        pytest.skip("examples/ not found; this suite runs from a cloudcc checkout")

    tree = ast.parse(path.read_text(), str(path))
    handles = persisted_handles(tree)
    if not handles:
        return

    problems = []
    for name, attr, line in attribute_uses(tree, handles):
        client = handles[name]
        if not hasattr(client, attr):
            problems.append(
                f"{path}:{line}: {name}.{attr} does not exist on "
                f"{client.__module__}.{client.__qualname__}, which is what "
                f"persist() returned"
            )
    assert not problems, "\n".join(problems)


def test_the_check_can_fail():
    """A test that cannot fail is worse than no test.

    The four real defects this was written for are fixed, so nothing in
    examples/ triggers it any more -- which means the only remaining evidence
    that it works is a case that still does.
    """
    tree = ast.parse(
        "import cloudcompiler as cloudcc\n"
        "from pathlib import Path\n"
        "audit = cloudcc.persist(Path('./x'), id='a')\n"
        "audit.write('n', b'')\n"
    )
    handles = persisted_handles(tree)
    assert handles == {"audit": pathlib.Path}

    missing = [
        attr
        for _, attr, _ in attribute_uses(tree, handles)
        if not hasattr(pathlib.Path, attr)
    ]
    assert missing == ["write"]
