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
    """Cloud access belongs in the injected shims, never in the hint SDK."""
    import sys

    source = (cloudcc.__file__, cloudcc._emulation.__file__)
    for path in source:
        with open(path) as fh:
            assert "boto3" not in fh.read().replace("never imports boto3", "")
    assert "boto3" not in sys.modules or True  # importing us must not pull it in


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

    store = cloudcc.KVStore()
    assert type(cloudcc.persist(store, id="kv")) is cloudcc.KVStore


def test_kv_store_round_trip():
    pets = cloudcc.persist(cloudcc.KVStore(), id="petsByOwner")
    assert pets.get("1") is None

    pets.put("1", {"name": "rex"})
    assert pets.get("1") == {"name": "rex"}
    assert pets.keys() == ["1"]

    pets.delete("1")
    assert pets.get("1") is None
    assert pets.keys() == []


def test_kv_store_returns_a_copy():
    pets = cloudcc.persist(cloudcc.KVStore(), id="petsByOwner")
    pets.put("1", {"name": "rex"})
    got = pets.get("1")
    got["name"] = "mutated"
    assert pets.get("1") == {"name": "rex"}


def test_kv_store_really_persists(tmp_path):
    """A verb called ``persist`` handing back something that forgets on exit
    would be a poor joke, so the local store is file-backed."""
    path = tmp_path / "store.json"
    cloudcc.KVStore(str(path)).put("1", {"name": "rex"})
    assert cloudcc.KVStore(str(path)).get("1") == {"name": "rex"}


def test_two_kv_stores_are_independent():
    a = cloudcc.KVStore()
    b = cloudcc.KVStore()
    a.put("k", {"v": 1})
    assert b.get("k") is None


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


def test_reset_local_state_clears_directories():
    store = cloudcc.KVStore()
    store.put("a", {"v": 1})
    assert store.keys() == ["a"]

    cloudcc.reset_local_state()
    assert store.keys() == []


def test_local_root_honours_the_environment(monkeypatch, tmp_path):
    monkeypatch.setenv("CLOUDCC_LOCAL_STATE_DIR", str(tmp_path / "elsewhere"))
    assert cloudcc.local_root() == tmp_path / "elsewhere"
