cmake_minimum_required(VERSION 3.20.0)
find_package(Zephyr REQUIRED HINTS $ENV{ZEPHYR_BASE})
project(dk_hci)
# Zephyr's H:4 bridge, unchanged (NCS v2.5.1); prj.conf, app.overlay and child_image/ size it for a PC host.
target_sources(app PRIVATE ${ZEPHYR_BASE}/samples/bluetooth/hci_uart/src/main.c)
