"""Unit "admin" -- Django on Fargate behind an ALB.

Django is the hard web framework for this compiler, and it is worth being
explicit about why rather than pretending otherwise:

  - the application object comes from `get_asgi_application()`, which does not
    exist until settings are configured. So `expose` is called *after*
    configuration, not at module top, and the compiler must accept that the
    exposed binding is produced by a call rather than a constructor;

  - the route table lives in `urlpatterns` in a module named by
    `ROOT_URLCONF`, not in decorators. Discovering it means resolving that
    module and reading the list, including `include()`. That is real work, and
    until it is done the honest fallback is a single proxy route (`/{proxy+}`)
    with a compile-time note saying so -- which loses edge-level rejection of
    unknown paths but is never *wrong*;

  - Django expects a long-lived process with connection pooling and
    `CONN_MAX_AGE`. That is a container, not a Lambda. Hence `type: ecs` in
    cloudcc.yaml, and a compiler that sees Django on `type: lambda` should say
    what it is about to cost rather than quietly agree.

shim: none for the framework itself. The container entry runs uvicorn against
this module's `application`, which is the same role the generated
`cloudcc_server_entry.mjs` plays for a Node container.
"""

from django.conf import settings as django_settings
from django.core.asgi import get_asgi_application
from django.http import JsonResponse
from django.urls import path

import cloudcompiler as cloudcc

from mega.orm import admin_db
from mega.storage import recent

cloudcc.execution_unit(id="admin")

# `admin_db` is the persisted DjangoDatabase from mega/orm.py. Uncompiled it
# describes a local Postgres; compiled it resolves to the provisioned one, with
# the password fetched on first query rather than stored here -- which matters
# because Django renders DATABASES on its own error pages.
django_settings.configure(
    DEBUG=False,
    ALLOWED_HOSTS=["*"],
    ROOT_URLCONF=__name__,
    DATABASES={"default": admin_db},
    INSTALLED_APPS=["django.contrib.contenttypes", "django.contrib.auth"],
    SECRET_KEY=cloudcc.config_value("django_secret_key", secret=True),
)


def dashboard(request) -> JsonResponse:
    return JsonResponse({"receipts": len(recent())})


def health(request) -> JsonResponse:
    return JsonResponse({"status": "ok"})


urlpatterns = [
    path("health", health),
    path("dashboard", dashboard),
]

application = get_asgi_application()
cloudcc.expose(application, id="admin-web")
