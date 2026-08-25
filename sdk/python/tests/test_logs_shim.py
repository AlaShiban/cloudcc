"""The logging shim is the one runtime module with no SDK counterpart.

There is nothing to compare it against, because there is nothing for a program
to declare: where logs go is chosen in cloudcc.yaml, and the call sites --
``logging.getLogger(__name__).info(...)`` -- are identical either way. So what
is worth testing is the contract the runtime owes an application: that by the
time user code runs, the root logger is pointed at the configured destination,
and that a destination this runtime cannot serve fails loudly instead of
logging somewhere nobody is looking.

It is loaded by path, like the other shims, because the SDK package does not
depend on the compiler's template tree. Unlike the others it imports nothing
but the standard library, so it can be executed rather than only parsed.
"""

import importlib.util
import json
import logging
import pathlib
import sys

import pytest

SHIM = (
    pathlib.Path(__file__).resolve().parents[3]
    / "internal"
    / "runtime"
    / "py"
    / "templates"
    / "_cloudcc_runtime"
    / "logs.py"
)


@pytest.fixture
def logs():
    if not SHIM.is_file():
        pytest.skip(f"{SHIM} not found; this test runs from a cloudcc checkout")
    spec = importlib.util.spec_from_file_location("_cloudcc_logs_under_test", SHIM)
    module = importlib.util.module_from_spec(spec)
    # Without this, importing a template leaves a __pycache__ beside it -- and
    # the template tree is embedded wholesale, so that .pyc would travel into
    # every compiled bundle. The compiler refuses to ship one either way; this
    # is so the checkout stays clean.
    previous, sys.dont_write_bytecode = sys.dont_write_bytecode, True
    try:
        spec.loader.exec_module(module)
    finally:
        sys.dont_write_bytecode = previous
    return module


@pytest.fixture(autouse=True)
def restore_root_logger():
    root = logging.getLogger()
    handlers, level = list(root.handlers), root.level
    yield
    root.handlers = handlers
    root.setLevel(level)


def test_cloudwatch_is_the_default(logs, monkeypatch):
    monkeypatch.delenv(logs.DESTINATION_ENV, raising=False)
    assert logs.destination() == "cloudwatch"


def test_lines_are_json_with_the_unit_attached(logs, monkeypatch, capsys):
    monkeypatch.setenv(logs.DESTINATION_ENV, "cloudwatch")
    logs.configure("api")

    logging.getLogger("mega").info("started")

    line = capsys.readouterr().out.strip()
    assert json.loads(line) == {
        "level": "info",
        "logger": "mega",
        "message": "started",
        # A module shared between units cannot know which unit is running it,
        # so the environment says and every line carries it.
        "unit": "api",
    }


def test_configure_replaces_the_handler_rather_than_adding_one(logs, monkeypatch, capsys):
    """Lambda installs its own root handler, which would double every record."""
    monkeypatch.setenv(logs.DESTINATION_ENV, "cloudwatch")
    logging.getLogger().addHandler(logging.StreamHandler())

    logs.configure("api")
    logging.getLogger("mega").info("once")

    assert len(capsys.readouterr().out.strip().splitlines()) == 1


def test_a_destination_this_runtime_cannot_serve_fails_loudly(logs, monkeypatch):
    """The compiler refuses these, so reaching one means the bundle and the
    configuration that deployed it disagree."""
    monkeypatch.setenv(logs.DESTINATION_ENV, "datadog")
    with pytest.raises(RuntimeError) as err:
        logs.configure("api")
    assert "datadog" in str(err.value) and "cloudwatch" in str(err.value)
