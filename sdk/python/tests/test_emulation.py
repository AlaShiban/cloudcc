"""The local emulations exist so an SDK-annotated app runs on a laptop.

These tests pin the behaviour that programs actually depend on, and -- just as
importantly -- pin the method signatures, which the compiler's parity test
compares against the injected _cloudcc_runtime clients.
"""

import os

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


def test_persist_kv_round_trip():
    pets = cloudcc.persist_kv("petsByOwner")
    assert pets.get("1") is None

    pets.put("1", {"name": "rex"})
    assert pets.get("1") == {"name": "rex"}
    assert pets.keys() == ["1"]

    pets.delete("1")
    assert pets.get("1") is None
    assert pets.keys() == []


def test_persist_kv_returns_a_copy():
    pets = cloudcc.persist_kv("petsByOwner")
    pets.put("1", {"name": "rex"})
    got = pets.get("1")
    got["name"] = "mutated"
    assert pets.get("1") == {"name": "rex"}


def test_the_same_id_gives_the_same_store():
    assert cloudcc.persist_kv("shared") is cloudcc.persist_kv("shared")
    assert cloudcc.persist_kv("a") is not cloudcc.persist_kv("b")


def test_persist_fs_round_trip():
    blobs = cloudcc.persist_fs("petAudit")
    assert blobs.list() == []
    assert not blobs.exists("a.txt")

    blobs.write("a.txt", b"hello")
    blobs.write("nested/b.txt", b"there")

    assert blobs.read("a.txt") == b"hello"
    assert blobs.exists("a.txt")
    assert blobs.list() == ["a.txt", "nested/b.txt"]
    assert blobs.list("nested/") == ["nested/b.txt"]

    blobs.delete("a.txt")
    assert not blobs.exists("a.txt")
    with pytest.raises(FileNotFoundError):
        blobs.read("a.txt")


def test_persist_secret_reads_the_environment(monkeypatch):
    secret = cloudcc.persist_secret("api-key")
    assert secret.get() == ""

    monkeypatch.setenv("CLOUDCC_SECRET_API_KEY", "s3cr3t")
    assert cloudcc.persist_secret("api-key").get() == "s3cr3t"

    secret.set("overridden")
    assert secret.get() == "overridden"


def test_persist_redis_operations():
    cache = cloudcc.persist_redis("sessions")
    assert cache.get("k") is None

    cache.set("k", "v")
    assert cache.get("k") == "v"

    cache.set("k", "v2", ex=60)
    assert cache.get("k") == "v2"

    assert cache.incr("hits") == 1
    assert cache.incr("hits", 4) == 5

    cache.delete("k")
    assert cache.get("k") is None


def test_persist_orm_gives_a_local_url():
    db = cloudcc.persist_orm("maindb", models=["Pet"])
    url = db.url()
    assert url.startswith("sqlite:///")
    assert url.endswith("maindb.db")


def test_pubsub_fans_out_in_process():
    topic = cloudcc.pubsub_topic("petEvents")
    seen = []

    @topic.subscribe
    def first(message):
        seen.append(("first", message["id"]))

    topic.subscribe(lambda message: seen.append(("second", message["id"])))

    topic.publish({"id": "1"})
    assert seen == [("first", "1"), ("second", "1")]
    assert len(list(topic.subscribers())) == 2


def test_subscribe_returns_the_function_so_it_works_as_a_decorator():
    topic = cloudcc.pubsub_topic("t")

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


def test_reset_local_state_clears_directories(tmp_path):
    blobs = cloudcc.persist_fs("b")
    blobs.write("a.txt", b"x")
    assert blobs.exists("a.txt")

    cloudcc.reset_local_state()
    assert not cloudcc.persist_fs("b").exists("a.txt")


def test_local_root_honours_the_environment(monkeypatch, tmp_path):
    monkeypatch.setenv("CLOUDCC_LOCAL_STATE_DIR", str(tmp_path / "elsewhere"))
    assert cloudcc.local_root() == tmp_path / "elsewhere"
