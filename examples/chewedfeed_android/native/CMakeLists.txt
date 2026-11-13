cmake_minimum_required(VERSION 3.22)
project(chewedfeed C CXX)

set(ARMS glasses CACHE STRING "glasses: the arms over Android's Bluetooth; emulated: two emulated arms")
set(SCOUTER_KIT ${CMAKE_CURRENT_SOURCE_DIR}/../../..)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

# Only ScouterKit's MIT library: no mosh, OpenSSL or protobuf.
add_library(scouter_kit STATIC ${SCOUTER_KIT}/src/base/scouter_kit.cpp ${SCOUTER_KIT}/src/g1/scouter_kit_g1.cpp
  ${SCOUTER_KIT}/src/vm/vm.c ${SCOUTER_KIT}/src/vm/vm_gfx.c)
target_include_directories(scouter_kit PUBLIC ${SCOUTER_KIT}/src)
if(ANDROID AND ARMS STREQUAL glasses)
  target_compile_definitions(scouter_kit PUBLIC SCOUTER_G1_NO_BLUEZ)
else()
  target_compile_definitions(scouter_kit PUBLIC SCOUTER_G1_EMULATOR)
endif()
find_package(Threads REQUIRED)
target_link_libraries(scouter_kit PUBLIC Threads::Threads)

add_library(chewedfeed_core STATIC boundary.cpp session.cpp)
target_include_directories(chewedfeed_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(chewedfeed_core PUBLIC scouter_kit)

if(ANDROID)
  add_library(chewedfeed SHARED jni.cpp link_transport.cpp)
  target_link_libraries(chewedfeed PRIVATE chewedfeed_core log)
  target_link_options(chewedfeed PRIVATE -Wl,--gc-sections)
else()
  # Host tests: cmake -S native -B BUILD && cmake --build BUILD && ctest --test-dir BUILD
  enable_testing()
  add_subdirectory(tests)
endif()
