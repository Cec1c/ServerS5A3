// Preserve the shared CALL instructions checked and temporarily owned by
// account statistics, ranking and battle pass. Chain at the predecessor
// dispatch functions instead, replaying exact whole instructions below.
extern "C" __declspec(dllexport) void* __cdecl PreviousFactory(int,void*);
extern "C" __declspec(dllexport) bool __cdecl PreviousLayout(const wchar_t*,void*);
#define KR_PREVIOUS_FACTORY PreviousFactory
#define KR_PREVIOUS_LAYOUT PreviousLayout
