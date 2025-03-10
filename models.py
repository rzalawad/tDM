from contextlib import contextmanager
from datetime import datetime, timezone

from sqlalchemy import Column, DateTime, Integer, String, create_engine
from sqlalchemy.orm import declarative_base, sessionmaker

Base = declarative_base()
_session_factory = None


class Download(Base):
    __tablename__ = "downloads"

    id = Column(Integer, primary_key=True, autoincrement=True)
    url = Column(String, nullable=False)
    directory = Column(String, nullable=False)
    status = Column(String, nullable=False)
    speed = Column(String)
    progress = Column(String)
    downloaded = Column(Integer)
    total_size = Column(Integer)
    date_added = Column(DateTime, default=lambda: datetime.now(timezone.utc))
    error = Column(String)
    gid = Column(String)


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
