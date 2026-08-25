"""The local emulations exist so an SDK-annotated app runs on a laptop.

These tests pin the behaviour that programs actually depend on, and -- just as
importantly -- pin the method signatures, which the compiler's parity test
compares against the injected _cloudcc_runtime clients.
"""

import pathlib

import pytest

import cloudcompiler as cloudcc


@pytest.fixture(autouse=True)
def isolated_state(tmp_path, monkeypatch):
    monkeypatch.setenv("CLOUDCC_LOCAL_STATE_DIR", str(tmp_path / "state"))
    cloudcc.reset_local_state()
    yield
    cloudcc.reset_local_state()


def test_the_sdk_never_imports_boto3():
    """Cloud access belongs in the injected shims, never in the hint SDK.

    Checked by reading the import statements rather than grepping the text: the
    documentation has to be free to name boto3, because wrapping a boto3 Table
    is now how a key/value store is declared. A test that cannot tell the
    difference between an import and a docstring would have to be weakened
    every time the docs improve.
    """
    import ast
    import sys

    for path in (cloudcc.__file__, cloudcc._emulation.__file__):
        with open(path) as fh:
            tree = ast.parse(fh.read(), path)
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                names = [a.name for a in node.names]
            elif isinstance(node, ast.ImportFrom):
                names = [node.module or ""]
            else:
                continue
            for name in names:
                assert not name.split(".")[0].startswith("boto"), (
                    f"{path} imports {name}: cloud access belongs in the "
                    f"injected runtime, not in the hint SDK"
                )

    assert "boto3" not in sys.modules  # importing us must not pull it in


def test_persist_returns_exactly_what_it_was_given():
    """The property the whole design rests on.

    ``persist`` is a compile-time hint. Uncompiled it must be the identity
    function, or a program would behave differently depending on whether it had
    been compiled -- which is the one thing this project cannot afford.
    """
    for client in [object(), {"a": 1}, [1, 2], "text", 42, pathlib.Path("./x")]:
        assert cloudcc.persist(client, id="anything") is client


def test_persist_preserves_the_type_it_was_handed():
    path = pathlib.Path("./docs")
    assert isinstance(cloudcc.persist(path, id="docs"), pathlib.Path)

    topic = cloudcc.Topic()
    assert type(cloudcc.persist(topic, id="events")) is cloudcc.Topic


def test_secret_reads_the_environment(monkeypatch):
    secret = cloudcc.persist(cloudcc.Secret(), id="api-key")
    assert secret.get() == ""

    monkeypatch.setenv("API_KEY", "s3cr3t")
    assert cloudcc.Secret("API_KEY").get() == "s3cr3t"

    secret.set("overridden")
    assert secret.get() == "overridden"


def test_topic_fans_out_in_process():
    topic = cloudcc.persist(cloudcc.Topic(), id="petEvents")
    seen = []

    @topic.subscribe
    def first(message):
        seen.append(("first", message["id"]))

    topic.subscribe(lambda message: seen.append(("second", message["id"])))

    topic.publish({"id": "1"})
    assert seen == [("first", "1"), ("second", "1")]
    assert len(list(topic.subscribers())) == 2


def test_subscribe_returns_the_function_so_it_works_as_a_decorator():
    topic = cloudcc.persist(cloudcc.Topic(), id="t")

    @topic.subscribe
    def handler(message):
        return "handled"

    assert handler({"id": "x"}) == "handled"


def test_config_value_reads_its_environment_variable(monkeypatch):
    assert cloudcc.config_value("log_level", default="info") == "info"
    monkeypatch.setenv("CLOUDCC_CONFIG_LOG_LEVEL", "debug")
    assert cloudcc.config_value("log_level", default="info") == "debug"


def test_config_value_secret_flag_is_compile_time_only(monkeypatch):
    monkeypatch.setenv("CLOUDCC_CONFIG_API_KEY", "abc")
    assert cloudcc.config_value("api_key", secret=True) == "abc"


def test_expose_returns_an_inert_handle(monkeypatch):
    app = object()
    gateway = cloudcc.expose(app, id="pet-api")
    assert gateway.id == "pet-api"
    assert gateway.target == "public"
    assert gateway.app is app
    assert gateway.url() == ""

    monkeypatch.setenv("CLOUDCC_GATEWAY_PET_API_URL", "https://example.test")
    assert gateway.url() == "https://example.test"


def test_hint_only_functions_return_quietly():
    assert cloudcc.execution_unit(id="api") is None
    assert cloudcc.execution_unit(id="api", type="ecs") is None
    assert cloudcc.static_unit("site", static_files="./public/**/*") is None
    assert cloudcc.embed_assets("./data/*.json") == "./data/*.json"


def test_reset_local_state_clears_directories(tmp_path, monkeypatch):
    monkeypatch.setenv("CLOUDCC_LOCAL_STATE_DIR", str(tmp_path / "state"))
    root = cloudcc.local_root()
    root.mkdir(parents=True, exist_ok=True)
    (root / "leftover").write_text("x")

    cloudcc.reset_local_state()
    assert not root.exists()


def test_local_root_honours_the_environment(monkeypatch, tmp_path):
    monkeypatch.setenv("CLOUDCC_LOCAL_STATE_DIR", str(tmp_path / "elsewhere"))
    assert cloudcc.local_root() == tmp_path / "elsewhere"


def test_the_sdk_supplies_no_data_store_classes():
    """A store is declared by wrapping the client library you already use.

    A class of ours would be a dialect nobody else speaks, and its methods
    would have to be kept in step with the injected runtime's forever -- which
    is the drift the parity test exists to catch and this rule exists to
    remove. What is left are the two capabilities that are not stores.
    """
    for gone in ["KVStore", "FileStore", "DocumentStore", "Queue"]:
        assert not hasattr(cloudcc, gone), (
            f"cloudcc.{gone} is a data store class; wrap a real client instead"
        )
    assert hasattr(cloudcc, "Topic")
    assert hasattr(cloudcc, "Secret")
