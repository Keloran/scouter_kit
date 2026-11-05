cmake_minimum_required(VERSION 3.20.0)
find_package(Zephyr REQUIRED HINTS $ENV{ZEPHYR_BASE})
project(g1min)
target_sources(app PRIVATE src/main.c src/g1_netcore.c)
