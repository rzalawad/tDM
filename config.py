import argparse
import logging
import os
import sys
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path
from typing import Any, Dict, Optional

import yaml

# Initialize logger
logger = logging.getLogger(__name__)


class Environment(Enum):
    """Application environment types"""

    DEVELOPMENT = "development"
    TESTING = "testing"
    PRODUCTION = "production"


@dataclass
class ServerConfig:
    """Server configuration settings"""

    host: str = "0.0.0.0"
    port: int = 54759

    def validate(self):
        """Validate server configuration"""
        if not isinstance(self.port, int) or self.port < 1 or self.port > 65535:
            raise ValueError(f"Invalid port number: {self.port}")
        return True


@dataclass
class DaemonConfig:
    """Daemon configuration settings"""

    concurrency: int = 1
    expire_downloads: str = "1d"
    mapper: Dict[str, str] = field(default_factory=dict)
    temporary_download_directory: Optional[str] = None
    aria2_options: Dict[str, str] = field(default_factory=dict)


    def validate(self):
        """Validate daemon configuration"""
        if not isinstance(self.concurrency, int) or self.concurrency < 1:
            raise ValueError(f"Invalid concurrency value: {self.concurrency}")

        # Validate temporary_download_directory if provided
        if self.temporary_download_directory:
            path = Path(self.temporary_download_directory)
            if not path.parent.exists():
                logger.warning(
                    f"Parent directory of temporary_download_directory does not exist: {path.parent}"
                )

        return True


@dataclass
class LoggingConfig:
    """Logging configuration settings"""

    level: str = "INFO"
    format: str = "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
    date_format: str = "%Y-%m-%d %H:%M:%S"

    def validate(self):
        """Validate logging configuration"""
        valid_levels = ["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"]
        if self.level.upper() not in valid_levels:
            raise ValueError(
                f"Invalid log level: {self.level}. Must be one of {valid_levels}"
            )
        return True

    def get_log_level(self):
        """Get the logging level as an integer"""
        return getattr(logging, self.level.upper(), logging.INFO)


@dataclass
class AppConfig:
    """Main application configuration"""

    environment: Environment = Environment.DEVELOPMENT
    database_path: str = "downloads.db"
    server: ServerConfig = field(default_factory=ServerConfig)
    daemon: DaemonConfig = field(default_factory=DaemonConfig)
    logging: LoggingConfig = field(default_factory=LoggingConfig)

    @classmethod
    def from_dict(cls, config_dict: Dict[str, Any]) -> "AppConfig":
        """Create a configuration object from a dictionary"""
        # Extract nested configs
        server_dict = config_dict.get("server", {})
        daemon_dict = config_dict.get("daemon", {})
        logging_dict = config_dict.get("logging", {})

        # Create the config object
        return cls(
            environment=Environment(
                config_dict.get("environment", "development")
            ),
            database_path=config_dict.get("database_path", "downloads.db"),
            server=ServerConfig(**server_dict)
            if server_dict
            else ServerConfig(),
            daemon=DaemonConfig(**daemon_dict)
            if daemon_dict
            else DaemonConfig(),
            logging=LoggingConfig(**logging_dict)
            if logging_dict
            else LoggingConfig(),
        )

    def validate(self) -> bool:
        """Validate the entire configuration"""
        try:
            self.server.validate()
            self.daemon.validate()
            self.logging.validate()

            # Validate database path
            db_path = Path(self.database_path)
            if not db_path.parent.exists():
                logger.warning(
                    f"Parent directory for database does not exist: {db_path.parent}"
                )

            return True
        except ValueError as e:
            logger.error(f"Configuration validation error: {e}")
            return False


class ConfigManager:
    """Manages application configuration"""

    _instance = None
    _config: Optional[AppConfig] = None

    def __new__(cls):
        """Singleton pattern to ensure only one config manager exists"""
        if cls._instance is None:
            cls._instance = super(ConfigManager, cls).__new__(cls)
        return cls._instance

    def load_config(
        self, config_path: Optional[str] = None, env_var: str = "CONFIG_PATH"
    ) -> AppConfig:
        """Load configuration from file and environment variables"""
        # Try to get config path from argument or environment variable
        config_path = config_path or os.getenv(env_var)

        # Start with default configuration
        config_dict = {
            "environment": os.getenv("APP_ENV", "development"),
            "database_path": "downloads.db",
            "server": {"host": "0.0.0.0", "port": 54759},
            "daemon": {
                "concurrency": 1,
                "mapper": {},
                "expire_downloads": "1d",
            },
            "logging": {"level": os.getenv("LOG_LEVEL", "INFO")},
        }

        # Load configuration from file if it exists
        if config_path and os.path.exists(config_path):
            logger.info(f"Loading configuration from file: {config_path}")
            try:
                with open(config_path, "r") as f:
                    file_config = yaml.safe_load(f)
                    if file_config:
                        self._deep_update(config_dict, file_config)
            except Exception as e:
                logger.error(f"Error loading configuration file: {e}")
                raise

        # Create and validate the configuration
        self._config = AppConfig.from_dict(config_dict)
        if not self._config.validate():
            logger.error("Configuration validation failed")
            raise ValueError("Invalid configuration")

        return self._config

    def _deep_update(self, d: Dict, u: Dict) -> Dict:
        """Recursively update a dictionary"""
        for k, v in u.items():
            if isinstance(v, dict) and k in d and isinstance(d[k], dict):
                self._deep_update(d[k], v)
            else:
                d[k] = v
        return d

    def get_config(self) -> AppConfig:
        """Get the current configuration"""
        if self._config is None:
            raise RuntimeError(
                "Configuration not loaded. Call load_config() first."
            )
        return self._config


def configure_logging(config: LoggingConfig) -> None:
    """Configure the root logger with the given configuration"""
    # Configure root logger
    root_logger = logging.getLogger()
    root_logger.handlers.clear()
    root_logger.setLevel(config.get_log_level())

    # Add console handler
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(config.get_log_level())
    formatter = logging.Formatter(config.format, datefmt=config.date_format)
    console_handler.setFormatter(formatter)
    root_logger.addHandler(console_handler)


def parse_args():
    """Parse command line arguments"""
    parser = argparse.ArgumentParser(
        description="Server and Daemon Configuration"
    )
    parser.add_argument("--config", type=str, help="Path to configuration file")
    parser.add_argument(
        "--log-level",
        type=str,
        help="Set the logging level (e.g., DEBUG, INFO, WARNING)",
    )
    parser.add_argument(
        "--env",
        type=str,
        choices=["development", "testing", "production"],
        help="Application environment",
    )
    return parser.parse_args()


# Global config manager instance
config_manager = ConfigManager()


def initialize_config():
    """Initialize configuration from command line arguments and environment variables"""
    args = parse_args()

    # Override environment variables with command line arguments if provided
    if args.log_level:
        os.environ["LOG_LEVEL"] = args.log_level
    if args.env:
        os.environ["APP_ENV"] = args.env

    # Load and validate configuration
    config = config_manager.load_config(args.config)

    # Set up logging
    configure_logging(config.logging)

    logger.info(
        f"Application initialized in {config.environment.value} environment"
    )
    return config


def get_config() -> AppConfig:
    """Get the current configuration or initialize if not loaded"""
    try:
        return config_manager.get_config()
    except RuntimeError:
        return initialize_config()
