import Image from "next/image";

export default function Home() {
  return <main>
    <h1>Rig Next.js hosting fixture</h1>
    <p>Public build marker: {process.env.NEXT_PUBLIC_BUILD_MARKER ?? "unset"}</p>
    <Image src="/pixel.png" alt="Fixture pixel" width={2} height={2} />
  </main>;
}
