#ifndef G1_NETFALLBACK_H_
#define G1_NETFALLBACK_H_

#include <stdbool.h>

/* Call once per boot before the net-core handshake. Returns true if a net-core reflash was set up
 * (the caller then reboots into MCUboot). */
bool netfallback_boot(void);
/* Call once BLE is up (bt_enable succeeded): clears the count of boots without Bluetooth. */
void netfallback_ble_up(void);

#endif
