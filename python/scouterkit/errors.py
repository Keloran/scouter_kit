class ScouterKitError(Exception):
    """Base class of the package's errors."""


class BluetoothError(ScouterKitError):
    """BlueZ failed, or the adapter is missing or off."""


class NoG1FoundError(ScouterKitError):
    """No glasses found."""


class BondingError(ScouterKitError):
    """Bonding with an arm failed."""


class FirmwareError(ScouterKitError):
    """The arms do not run the scouter firmware (protocol 5)."""


class NotInitializedError(ScouterKitError):
    """The call needs init() first."""


class NoReplyError(ScouterKitError):
    """The arm did not reply, or refused the request."""


class ProgramError(ScouterKitError, ValueError):
    """A program that cannot be loaded: an unbound label, a full pool, ..."""


class DeviceMemoryError(ScouterKitError, MemoryError):
    """No room left in the glasses' memory."""
