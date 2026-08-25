"""Unit "ops" -- a command line tool, which is a unit of a different shape.

A CLI is not a server. It has no port, nothing routes to it, and it exits. But
it is not *nothing* either: it needs the same database, the same secrets and
the same permissions as the units that serve traffic, and today the way people
run one against production is to copy credentials onto a laptop.

So the proposal is a third execution unit type -- run-to-completion:

    execution_units:
      ops:
        type: task          # ECS RunTask, or a Lambda for short commands

and a way to invoke it:

    cloudcc run ops -- backfill --since 2026-01-01

which starts the task with the unit's own role, streams its output back, and
exits with its exit code. No laptop credentials, no bastion, and the same
bindings the application has -- because it is the same compiled bundle.

What the compiler does with the three CLI libraries below is the same for all
of them: nothing to the code, and it reads the command tree so that
`cloudcc run ops --help` can list the commands without starting a container.
Click and Typer both expose that tree statically (`@app.command`,
`@click.command`); argparse builds it imperatively, so for argparse the honest
fallback is to pass arguments through unexamined.
"""

import argparse

import click
import typer

import cloudcompiler as cloudcc

from mega.jobs import rebuild_search_index
from mega.orm import engine
from mega.storage import recent

# proposed: `type="task"` -- run-to-completion, not a server.
cloudcc.execution_unit(id="ops", type="task")

cli = typer.Typer(help="Operational commands for mega-app.")


@cli.command()
def backfill(since: str, dry_run: bool = False) -> None:
    """Re-index every order placed since a date.

    Typer derives the flags from the annotations, which is also what lets
    `cloudcc run` know that --since takes a value without importing anything.
    """
    with engine.connect() as conn:
        rows = conn.exec_driver_sql(
            "SELECT id FROM \"order\" WHERE placed_at >= %s", (since,)
        ).fetchall()
    typer.echo(f"{len(rows)} orders since {since}")
    if dry_run:
        return
    for (order_id,) in rows:
        rebuild_search_index.send(order_id)


@cli.command()
def receipts() -> None:
    """List stored receipts. Reads the persisted bucket, not a local folder."""
    for name in recent():
        typer.echo(name)


@click.command()
@click.option("--limit", default=10, show_default=True)
def tail(limit: int) -> None:
    """A Click command, for the case where a program already has some.

    Click and Typer coexist -- Typer is built on Click -- so a program
    migrating from one to the other has both, and the compiler should read both
    trees rather than insist on a single framework.
    """
    for name in recent()[-limit:]:
        click.echo(name)


def argparse_entry(argv: list[str] | None = None) -> int:
    """The standard-library case.

    argparse builds its parser imperatively, so the command tree is only known
    by running this function. `cloudcc run` therefore passes arguments through
    unexamined and lets argparse report its own errors -- which is worse than
    the other two only in that `--help` costs a container start.
    """
    parser = argparse.ArgumentParser(prog="mega-ops")
    parser.add_argument("--vacuum", action="store_true")
    args = parser.parse_args(argv)
    if args.vacuum:
        with engine.connect() as conn:
            conn.exec_driver_sql("VACUUM ANALYZE")
    return 0


if __name__ == "__main__":
    cli()
