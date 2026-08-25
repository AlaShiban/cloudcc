"""SQLModel tables.

Worth its own module because of what the compiler can do with it: every table
is a `SQLModel` subclass with `table=True`, and they are all registered in
`SQLModel.metadata`. So for this one ORM the `models=` argument on `persist` is
redundant -- the schema can be *discovered* rather than declared, and the
compile-time check that code and schema have not drifted comes for free.

cloudcc still does not run DDL. Knowing the tables is not the same as owning
them, and a compiler that silently altered a production schema on deploy is a
compiler nobody should run.
"""

from decimal import Decimal

from sqlmodel import Field, SQLModel


class Order(SQLModel, table=True):
    id: str = Field(primary_key=True)
    customer_id: str
    total: Decimal


class Refund(SQLModel, table=True):
    id: str = Field(primary_key=True)
    order_id: str
    reason: str
