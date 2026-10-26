// ATT long reads request successive offsets. Never repeat the full identity
// for a read-blob request or return more than the requested response length.
static class IdentityRead {
    public static bool TrySlice(byte[] value,uint offset,uint length,out byte[] response) {
        response=Array.Empty<byte>();
        if(offset>value.Length)return false;
        var count=(int)Math.Min(length,(uint)value.Length-offset);
        response=value.AsSpan((int)offset,count).ToArray();
        return true;
    }
}
