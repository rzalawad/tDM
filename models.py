from datetime import datetime, timezone

from sqlalchemy import Column, DateTime, Float, Integer, String, create_engine
from sqlalchemy.ext.declarative import declarative_base
from sqlalchemy.orm import sessionmaker

from config import get_config

Base = declarative_base()


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


class DaemonSettings(Base):
    __tablename__ = "daemon_settings"

    id = Column(Integer, primary_key=True)
    concurrency = Column(Integer, default=1)


config = get_config()
engine = create_engine(f"sqlite:///{config['database_path']}")
Base.metadata.create_all(engine)
Session = sessionmaker(bind=engine)
