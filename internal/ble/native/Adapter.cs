using System.Text;
using System.Text.Json;
using Windows.Devices.Bluetooth;
using Windows.Devices.Bluetooth.Advertisement;
using Windows.Devices.Bluetooth.GenericAttributeProfile;
using Windows.Storage.Streams;

static class Adapter {
    static readonly Guid Service = Guid.Parse("c9e0b001-49f0-4c98-9c83-3ad797a40d10");
    static readonly Guid Identity = Guid.Parse("c9e0b002-49f0-4c98-9c83-3ad797a40d10");
    static readonly Guid RX = Guid.Parse("c9e0b003-49f0-4c98-9c83-3ad797a40d10");
    static readonly Guid TX = Guid.Parse("c9e0b004-49f0-4c98-9c83-3ad797a40d10");
    static readonly object OutputLock = new();
    static byte[] value = new byte[8];
    static BluetoothLEDevice? device;
    static GattDeviceService? remoteService;
    static GattCharacteristic? rx, tx;
    static GattServiceProvider? provider;
    static BluetoothLEAdvertisementWatcher? watcher;
    static readonly SemaphoreSlim Connecting = new(1);
    static readonly HashSet<ulong> Seen = new();
    static string? owner;
    static readonly object OwnerLock = new();
    static void Emit(object o) { lock(OutputLock) {Console.WriteLine(JsonSerializer.Serialize(o));Console.Out.Flush();} }
    static void Fail(string text) {Emit(new { @event="error",error=text });Environment.Exit(1);}
    static byte[] Bytes(IBuffer b) {using var r=DataReader.FromBuffer(b);var data=new byte[b.Length];r.ReadBytes(data);return data;}
    static IBuffer Buffer(byte[] b) {using var w=new DataWriter();w.WriteBytes(b);return w.DetachBuffer();}
    static void Check(GattCommunicationStatus s) {if(s!=GattCommunicationStatus.Success)throw new IOException("GATT: "+s);}
    static async Task Listen(string id,string name,bool pairing) {
        var adapter=await BluetoothAdapter.GetDefaultAsync();
        if(adapter==null || !adapter.IsPeripheralRoleSupported)throw new IOException("This Bluetooth adapter/driver does not support BLE peripheral mode");
        var result=await GattServiceProvider.CreateAsync(Service);
        if(result.Error!=BluetoothError.Success)throw new IOException("GATT service: "+result.Error);
        provider=result.ServiceProvider;
        var info=await provider.Service.CreateCharacteristicAsync(Identity,new GattLocalCharacteristicParameters {
            CharacteristicProperties=GattCharacteristicProperties.Read,
            ReadProtectionLevel=GattProtectionLevel.Plain,
            StaticValue=Buffer(JsonSerializer.SerializeToUtf8Bytes(new{id,name,pairing})) });
        if(info.Error!=BluetoothError.Success)throw new IOException("Identity characteristic: "+info.Error);
        var r=await provider.Service.CreateCharacteristicAsync(RX,new GattLocalCharacteristicParameters {
            CharacteristicProperties=GattCharacteristicProperties.Write, WriteProtectionLevel=GattProtectionLevel.Plain });
        var t=await provider.Service.CreateCharacteristicAsync(TX,new GattLocalCharacteristicParameters {
            CharacteristicProperties=GattCharacteristicProperties.Read, ReadProtectionLevel=GattProtectionLevel.Plain });
        if(r.Error!=BluetoothError.Success || t.Error!=BluetoothError.Success)throw new IOException("Cannot create mailbox characteristics");
        r.Characteristic.WriteRequested+=async (_,args)=>{
            var def=args.GetDeferral();
            try {
                var req=await args.GetRequestAsync();if(req==null)return;
                var who=args.Session.DeviceId.Id;
                lock(OwnerLock){if(owner!=null && owner!=who){req.RespondWithProtocolError(0x0e);return;}owner=who;}
                var b=Bytes(req.Value);
                if(req.Offset!=0 || b.Length<8 || b.Length>20){req.RespondWithProtocolError(0x0d);return;}
                Emit(new{@event="packet",data=Convert.ToBase64String(b)});req.Respond();
            } catch(Exception e){Fail("GATT write request: "+e.Message);} finally{def.Complete();}
        };
        t.Characteristic.ReadRequested+=async (_,args)=>{
            var def=args.GetDeferral();
            try {var req=await args.GetRequestAsync();if(req==null)return;
                if(owner!=null && owner!=args.Session.DeviceId.Id){req.RespondWithProtocolError(0x0e);return;}
                req.RespondWithValue(Buffer(Volatile.Read(ref value)));
            }catch(Exception e){Fail("GATT read request: "+e.Message);}finally{def.Complete();}
        };
        var ready=new TaskCompletionSource();
        provider.AdvertisementStatusChanged+=(_,args)=>{
            if(args.Status==GattServiceProviderAdvertisementStatus.Started)ready.TrySetResult();
            if(args.Status==GattServiceProviderAdvertisementStatus.Aborted)ready.TrySetException(new IOException("BLE advertisement aborted: "+args.Error));
        };
        provider.StartAdvertising(new GattServiceProviderAdvertisingParameters {IsConnectable=true,IsDiscoverable=true});
        await ready.Task.WaitAsync(TimeSpan.FromSeconds(20));Emit(new{@event="ready"});
    }
    static void Scan(string mode,string target) {
        watcher=new BluetoothLEAdvertisementWatcher {ScanningMode=BluetoothLEScanningMode.Active};
        watcher.AdvertisementFilter.Advertisement.ServiceUuids.Add(Service);
        watcher.Stopped+=(_,args)=>{if(args.Error!=BluetoothError.Success)Fail("Bluetooth scan stopped: "+args.Error);};
        watcher.Received+=(sender,args)=>{_ = Inspect(args.BluetoothAddress,mode,target);};watcher.Start();
    }
    static bool MarkSeen(ulong address){lock(Seen){return Seen.Add(address);}}
    static async Task Inspect(ulong address,string mode,string target) {
        if(!MarkSeen(address))return;
        await Connecting.WaitAsync();
        try {
            if(device!=null)return;
            using var timeout=new CancellationTokenSource(TimeSpan.FromSeconds(10));
            // AsTask cancellation bounds discovery even for stale advertisements.
            var d=await System.WindowsRuntimeSystemExtensions.AsTask(BluetoothLEDevice.FromBluetoothAddressAsync(address),timeout.Token);
            if(d==null)return;
            bool keep=false;
            try {
                var ss=await d.GetGattServicesForUuidAsync(Service,BluetoothCacheMode.Uncached);Check(ss.Status);
                foreach(var s in ss.Services) {
                    var cs=await s.GetCharacteristicsAsync(BluetoothCacheMode.Uncached);Check(cs.Status);
                    var identity=cs.Characteristics.FirstOrDefault(c=>c.Uuid==Identity);
                    if(identity==null){s.Dispose();continue;}
                    var read=await identity.ReadValueAsync(BluetoothCacheMode.Uncached);Check(read.Status);
                    using var doc=JsonDocument.Parse(Bytes(read.Value));var root=doc.RootElement;
                    var id=root.GetProperty("id").GetString();var name=root.GetProperty("name").GetString();
                    if(mode=="scan")Emit(new{@event="peer",id,name,pairing=root.TryGetProperty("pairing",out var flag) && flag.GetBoolean()});
                    else if(id==target) {
                        rx=cs.Characteristics.First(c=>c.Uuid==RX);tx=cs.Characteristics.First(c=>c.Uuid==TX);
                        device=d;remoteService=s;keep=true;watcher!.Stop();
                        d.ConnectionStatusChanged+=(_,_)=>{if(d.ConnectionStatus==BluetoothConnectionStatus.Disconnected)Fail("Bluetooth peer disconnected");};
                        Emit(new{@event="ready"});return;
                    }
                    s.Dispose();
                }
            }finally{if(!keep)d.Dispose();}
        } catch { /* An unrelated or vanished advertisement must not terminate discovery. */ }
        finally{Connecting.Release();await Task.Delay(2000);lock(Seen){Seen.Remove(address);}}
    }
    public static async Task Main(string[] args) {
        try {
            if(args[0]=="listen" || args[0]=="pair-listen")await Listen(args[1],args[2],args[0]=="pair-listen");else Scan(args[0],args[1]);
            string? line;
            while((line=await Console.In.ReadLineAsync())!=null) {
                using var doc=JsonDocument.Parse(line);var root=doc.RootElement;
                var data=Convert.FromBase64String(root.GetProperty("data").GetString()!);
                switch(root.GetProperty("op").GetString()) {
                    case "value":Volatile.Write(ref value,data);break;
                    case "exchange":
                        if(rx==null || tx==null)throw new IOException("Not connected");
                        Check(await rx.WriteValueAsync(Buffer(data),GattWriteOption.WriteWithResponse));
                        var result=await tx.ReadValueAsync(BluetoothCacheMode.Uncached);Check(result.Status);
                        Emit(new{@event="packet",data=Convert.ToBase64String(Bytes(result.Value))});break;
                    default:throw new IOException("Unknown command");
                }
            }
        }catch(Exception e){Fail(e.Message);}
        finally{watcher?.Stop();provider?.StopAdvertising();remoteService?.Dispose();device?.Dispose();}
    }
}
