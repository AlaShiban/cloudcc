"""What this package claims about itself, checked against what it is.

`pyproject.toml` said 0.1.0 while `cloudcompiler.__version__` said 0.2.0, so
`pip show cloudcompiler` and the package itself disagreed, and neither was
obviously the authoritative one. Version numbers drift exactly like this:
quietly, in the file nobody reads while changing the one they do.

The Node half carries the same assertions in `sdk/node/test/packaging.test.js`,
including the cross-check that the two SDKs report one version -- they are two
halves of one release.
"""

import pathlib
import re

import cloudcompiler

PYPROJECT = pathlib.Path(__file__).resolve().parents[1] / "pyproject.toml"


def _pyproject_field(name):
    match = re.search(rf'^{name} = "([^"]+)"', PYPROJECT.read_text(), re.M)
    assert match, f"{name} is missing from pyproject.toml"
    return match.group(1)


def test_the_packaged_version_is_the_one_the_module_reports():
    assert _pyproject_field("version") == cloudcompiler.__version__, (
        "pyproject.toml and cloudcompiler.__version__ disagree; `pip show` would "
        "report one and the running package the other"
    )


def test_the_readme_the_package_ships_exists():
    readme = PYPROJECT.parent / _pyproject_field("readme")
    assert readme.is_file(), (
        f"pyproject.toml names {readme.name} as its readme, and it is not there, "
        "so the package would publish without one"
    )


def test_every_public_name_is_actually_exported():
    """`__all__` is documentation with the authority of an import.

    A name listed here and missing from the module is an ImportError for anyone
    following the docs, and the two drift whenever a capability is renamed.
    """
    for name in cloudcompiler.__all__:
        assert hasattr(cloudcompiler, name), (
            f"cloudcompiler.__all__ promises {name!r}, which the module does not define"
        )
