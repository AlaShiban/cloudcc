"""Configuration -- the three ways Python programs read it.

The whole category shares one property that makes it easy: every one of these
libraries ultimately reads ``os.environ``. So there is no client to talk to and
no wire protocol to intercept. What the compiler needs from this file is only
the *key set*: which names exist, which are secret, and what the defaults are.
Once it knows that, it can provision them and put them in the environment
before user code runs, and every library below finds them without a shim.

That is why these are declared with ``persist`` even though nothing here is
stateful in the storage sense: the verb means "this outlives the process", and
configuration does. What the wrapped type declares is the capability ``config``.
"""

from dotenv import load_dotenv
from dynaconf import Dynaconf
from pydantic import SecretStr
from pydantic_settings import BaseSettings

import cloudcompiler as cloudcc

# Uncompiled this reads .env from the working directory. Compiled it finds no
# file and does nothing, which is correct rather than merely harmless: the
# values are already in os.environ by the time this module is imported.
#
# shim: none. This call is left exactly as written. A compiler that erased it
# would break the uncompiled run, and the uncompiled run is the thing the whole
# design is trying to keep working.
load_dotenv()


class Settings(BaseSettings):
    """Typed configuration. The field set is the contract with the compiler."""

    log_level: str = "info"
    page_size: int = 50
    checkout_timeout_seconds: int = 30

    # No default, so it must come from somewhere. Compiled, that somewhere is
    # Secrets Manager; uncompiled it is .env.
    stripe_key: SecretStr

    # A default that is a local address is the honest way to write this: it is
    # what the uncompiled program should use. The compiled program gets the
    # real endpoint, and the value here is never deployed.
    database_url: str = "postgresql://localhost/mega"


# shim: none at runtime -- BaseSettings already reads os.environ, and the
# injected runtime has populated it. What the *compiler* does with this:
#
#   - reads the class body at compile time to learn the fields;
#   - each non-secret field becomes a config value, materialised as an
#     environment variable on every unit that imports this module;
#   - each SecretStr field becomes a Secrets Manager secret. It is a Pulumi
#     stack secret, so it is not in state in plaintext, not in any env file
#     written to disk, and not in logs (D21). The unit gets read permission on
#     that one secret and nothing else;
#   - a field with no default and no configured value is a *compile* error,
#     not a KeyError on the first request in production.
#
# open question: pydantic-settings resolves at instantiation, which happens at
# import. A secret fetched over the network at import time costs cold-start
# latency on every Lambda instance. Either the injected runtime fetches the
# unit's secrets once during init (before user modules load), or SecretStr
# fields need to be lazy. The first is simpler and is what this assumes.
settings = cloudcc.persist(Settings(), id="settings")


# Dynaconf is the multi-source case: files plus environment. The file is in the
# source tree, so the compiler can read it at compile time exactly as it reads
# the class above -- the key set is static either way.
#
# shim: none. The compiler:
#   - parses settings.toml to learn the keys, and treats each as a config value
#     whose default is the value in the file;
#   - copies settings.toml into the bundle, so uncompiled and compiled read the
#     same file and only the overrides differ;
#   - uses envvar_prefix to know what to name the injected variables, because
#     that prefix is the only thing that tells it MEGA_RETRY_LIMIT and
#     retry_limit are the same key.
ops_settings = cloudcc.persist(
    Dynaconf(settings_files=["settings.toml"], envvar_prefix="MEGA"),
    id="opsSettings",
)
