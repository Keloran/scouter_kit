add_executable(boundary_test boundary_test.cpp)
target_link_libraries(boundary_test PRIVATE chewedfeed_core)
add_test(NAME boundary COMMAND boundary_test)
