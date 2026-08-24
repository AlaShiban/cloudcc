"""Shared boto3 plumbing: endpoint override, region, and dummy credentials."""

import os

import boto3


def _common():
    """Keyword arguments every boto3 constructor gets."""
    kwargs = {}

    endpoint = os.environ.get("CLOUDCC_AWS_ENDPOINT_URL")
    if endpoint:
        kwargs["endpoint_url"] = endpoint

    region = os.environ.get("AWS_REGION") or os.environ.get("AWS_DEFAULT_REGION")
    if region:
        kwargs["region_name"] = region

    # Emulators accept any credentials but boto3 refuses to sign without some.
    # Supplying placeholders only when an endpoint override is in play keeps
    # real deployments on the normal credential chain.
    if endpoint and not os.environ.get("AWS_ACCESS_KEY_ID"):
        kwargs["aws_access_key_id"] = "cloudcc-local"
        kwargs["aws_secret_access_key"] = "cloudcc-local"

    return kwargs


def client(service):
    """A boto3 client for ``service``."""
    return boto3.client(service, **_common())


def resource(service):
    """A boto3 resource for ``service``."""
    return boto3.resource(service, **_common())


def slug(id):
    """The environment-variable spelling of a capability id.

    Must agree with internal/sanitize.EnvVar in the compiler; the compiler's
    parity test pins the two together.
    """
    return "".join(c.upper() if c.isalnum() else "_" for c in id)


def env(name, capability, id):
    """Read a required environment binding, or explain what is missing."""
    try:
        return os.environ[name]
    except KeyError:
        raise RuntimeError(
            "%s is not set: this process was not wired to the %s %r. "
            "Environment bindings come from the generated Pulumi stack; when "
            "running locally, export them from `pulumi output --json`."
            % (name, capability, id)
        ) from None
