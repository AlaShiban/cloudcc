"""The worker's own relational store, reached through the *synchronous* ORM.

A second database rather than a second view of the first, and deliberately so.
The worker has no business reading the pet catalogue, and because permissions
and environment are both derived from what a unit bundles, not importing
``shared/catalogue.py`` is how it ends up without access to ``petsdb`` or to the
cache. Handing it a session on the same database would throw that away.

What it does demonstrate is the other half of a pair. ``shared/catalogue.py``
declares ``create_async_engine`` and gets an ``AsyncEngine`` back; this file
declares ``create_engine`` and gets an ``Engine``. They are different libraries
with different call conventions, and the compiler knows which one each unit
asked for -- so one application ends up with a Lambda that awaits its queries
and a Lambda that does not, from the same compiler and the same SDK verb.

Uncompiled this talks to a local Postgres:

    docker run -d -p 5432:5432 -e POSTGRES_USER=ccadmin \\
      -e POSTGRES_DB=auditdb -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16-alpine
"""

from sqlalchemy import create_engine, func, select
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column

import cloudcompiler as cloudcc


class Base(DeclarativeBase):
    pass


class Audit(Base):
    """One audited event: which pet, and the signature that was written."""

    __tablename__ = "audits"

    pet_id: Mapped[str] = mapped_column(primary_key=True)
    summary: Mapped[str] = mapped_column(default="")
    signature: Mapped[str] = mapped_column(default="")
    revision: Mapped[int] = mapped_column(default=0)


engine = cloudcc.persist(
    create_engine("postgresql://ccadmin@localhost:5432/auditdb"),
    id="auditdb",
    models=["Audit"],
)

_schema_ready = False


def ensure_schema() -> None:
    """Create the table if it is not there.

    Guarded rather than run at import: `create_all` is idempotent but not free,
    and a Lambda pays for it on every cold start either way.
    """
    global _schema_ready
    if not _schema_ready:
        Base.metadata.create_all(engine)
        _schema_ready = True


def record(pet_id: str, summary: str, signature: str) -> int:
    """Write one audit row, and return how many times this pet has been seen."""
    ensure_schema()
    with Session(engine) as session:
        row = session.get(Audit, pet_id)
        if row is None:
            row = Audit(pet_id=pet_id, revision=0)
            session.add(row)
        row.summary = summary
        row.signature = signature
        row.revision += 1
        session.commit()
        return row.revision


def audited() -> int:
    """How many distinct pets the ledger holds."""
    ensure_schema()
    with Session(engine) as session:
        return int(session.scalar(select(func.count()).select_from(Audit)) or 0)
