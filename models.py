from contextlib import contextmanager
from datetime import datetime, timezone
from enum import Enum

from sqlalchemy import (
    Column,
    DateTime,
    ForeignKey,
    Integer,
    String,
    create_engine,
)
from sqlalchemy import (
    Enum as SQLAEnum,
)
from sqlalchemy.orm import declarative_base, relationship, sessionmaker

Base = declarative_base()
_session_factory = None


class TaskType(Enum):
    UNPACK = "unpack"
    MOVE = "move"


class Status(Enum):
    PENDING = "pending"
    SUBMITTED = "submitted"
    DOWNLOADING = "downloading"
    COMPLETED = "completed"
    FAILED = "failed"
    MOVING = "moving"
    UNPACKING = "unpacking"


class GroupStatus(Enum):
    PENDING = "pending"
    COMPLETED = "completed"
    FAILED = "failed"
    UNPACKING = "unpacking"


TASK_TYPE_TO_STATUS = {
    TaskType.UNPACK: Status.UNPACKING,
    TaskType.MOVE: Status.MOVING,
}
STATUS_TO_TASK_TYPE = {v: k for k, v in TASK_TYPE_TO_STATUS.items()}


class Group(Base):
    __tablename__ = "groups"

    id = Column(Integer, primary_key=True, autoincrement=True)
    task = Column(SQLAEnum(TaskType), nullable=True)
    status = Column(SQLAEnum(GroupStatus), nullable=False)
    error = Column(String)

    downloads = relationship("Download", back_populates="group")
    tasks = relationship("Task", back_populates="group")


class Task(Base):
    __tablename__ = "tasks"

    id = Column(Integer, primary_key=True, autoincrement=True)
    task_type = Column(SQLAEnum(TaskType), nullable=False)
    group_id = Column(Integer, ForeignKey("groups.id"))
    status = Column(SQLAEnum(Status), nullable=False)
    error = Column(String)

    group = relationship("Group", back_populates="tasks")


class Download(Base):
    __tablename__ = "downloads"

    id = Column(Integer, primary_key=True, autoincrement=True)
    url = Column(String, nullable=False)
    directory = Column(String, nullable=False)
    status = Column(SQLAEnum(Status), nullable=False)
    speed = Column(String)
    progress = Column(String)
    downloaded = Column(Integer)
    total_size = Column(Integer)
    error = Column(String)
    date_added = Column(DateTime, default=lambda: datetime.now(timezone.utc))
    gid = Column(String)
    group_id = Column(Integer, ForeignKey("groups.id"))

    group = relationship("Group", back_populates="downloads")


class DaemonSettings(Base):
    __tablename__ = "daemon_settings"

    id = Column(Integer, primary_key=True)
    concurrency = Column(Integer, default=1)


def init_db(db_path):
    global _session_factory
    engine = create_engine(f"sqlite:///{db_path}")
    Base.metadata.create_all(engine)
    _session_factory = sessionmaker(bind=engine)


def get_session():
    return _session_factory()


@contextmanager
def session_scope():
    """Provide a transactional scope around a series of operations."""
    session = _session_factory()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()
