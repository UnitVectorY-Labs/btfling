using System.Text;

// Reconstruct a long identity as a central would read it at several ATT MTUs,
// including exact multiples, an empty final chunk, and invalid offsets.
var value=Encoding.UTF8.GetBytes("{\"version\":1,\"id\":\"0123456789abcdef0123456789abcdef\",\"name\":\"office-laptop\",\"pairing\":true,\"accepting\":true}");
foreach(var size in new[]{1,22,23,64,185,512}) {
    using var result=new MemoryStream();
    uint offset=0;
    while(true) {
        if(!IdentityRead.TrySlice(value,offset,(uint)size,out var chunk))throw new Exception("Valid long read rejected");
        if(chunk.Length>size)throw new Exception("Read exceeded requested length");
        result.Write(chunk);offset+=(uint)chunk.Length;
        if(chunk.Length<size)break;
    }
    if(!result.ToArray().SequenceEqual(value))throw new Exception("Long read corrupted the identity");
}
if(!IdentityRead.TrySlice(value,(uint)value.Length,22,out var end) || end.Length!=0)throw new Exception("End offset must return empty value");
if(IdentityRead.TrySlice(value,(uint)value.Length+1,22,out _))throw new Exception("Invalid offset accepted");
if(IdentityRead.TrySlice(value,uint.MaxValue,uint.MaxValue,out _))throw new Exception("Overflowing offset accepted");
if(!IdentityRead.TrySlice(value,0,0,out var empty) || empty.Length!=0)throw new Exception("Zero-length request failed");
Console.WriteLine("Identity long-read tests passed.");

var aborted=new AdvertisingLifecycle();
aborted.Observe("Created",null);
aborted.Observe("Aborted",null);
try {await aborted.Ready;throw new Exception("Aborted advertising was accepted");}catch(IOException e){
    if(e.Message.Contains("Success") || !e.Message.Contains("no Bluetooth error code"))throw new Exception("Misleading abort diagnostic");
}
if(!aborted.Failure.IsCompleted)throw new Exception("Abort did not signal listener failure");

var partial=new AdvertisingLifecycle();
partial.Observe("StartedWithoutAllAdvertisementData",null);
await partial.Ready;
partial.Observe("Aborted","RadioNotAvailable (5)");
if(!partial.Failure.IsCompleted)throw new Exception("Abort after starting was ignored");
if(!(await partial.Failure).Message.Contains("RadioNotAvailable"))throw new Exception("Reported radio error was lost");

var stopped=new AdvertisingLifecycle();
stopped.Observe("Started",null);await stopped.Ready;
stopped.StopRequested();stopped.Observe("Stopped",null);stopped.Observe("Aborted",null);
if(stopped.Failure.IsCompleted)throw new Exception("Intentional stop was treated as a radio failure");
Console.WriteLine("Advertising lifecycle tests passed.");
