/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useEffect, useRef } from 'react'
import { Link } from '@tanstack/react-router'
import {
  ArrowRight,
  BookOpen,
  Braces,
  CircleDot,
  Command,
  Hexagon,
  KeyRound,
  Orbit,
  RadioTower,
  Sparkles,
  Zap,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import * as THREE from 'three'
import { useAuthStore } from '@/stores/auth-store'
import { Markdown } from '@/components/ui/markdown'
import { PublicLayout } from '@/components/layout'
import './galaxy-home.css'
import { useHomePageContent } from './hooks'

const providers = [
  'OpenAI',
  'Claude',
  'Gemini',
  'DeepSeek',
  'Qwen',
  'Azure',
  'Bedrock',
  'Cohere',
  'Mistral',
  'Vertex',
]

const metrics = [
  { label: 'active providers', value: '40+' },
  { label: 'routing success', value: '99.95%' },
  { label: 'p95 latency', value: '112ms' },
]

const layers = [
  { title: 'Model exchange', detail: 'OpenAI-compatible gateway', icon: Orbit },
  { title: 'Policy router', detail: 'Latency, price, fallback', icon: Command },
  { title: 'Quota fabric', detail: 'Keys, groups, budgets', icon: KeyRound },
]

function createGlowTexture() {
  const canvas = document.createElement('canvas')
  canvas.width = 96
  canvas.height = 96
  const ctx = canvas.getContext('2d')
  if (!ctx) return null

  const gradient = ctx.createRadialGradient(48, 48, 0, 48, 48, 48)
  gradient.addColorStop(0, 'rgba(255,255,255,1)')
  gradient.addColorStop(0.25, 'rgba(94,234,212,0.95)')
  gradient.addColorStop(0.58, 'rgba(59,130,246,0.34)')
  gradient.addColorStop(1, 'rgba(0,0,0,0)')
  ctx.fillStyle = gradient
  ctx.fillRect(0, 0, 96, 96)

  const texture = new THREE.CanvasTexture(canvas)
  texture.colorSpace = THREE.SRGBColorSpace
  return texture
}

function NexusCanvas() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const renderer = new THREE.WebGLRenderer({
      alpha: true,
      antialias: true,
      canvas,
      powerPreference: 'high-performance',
    })
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2))
    renderer.setClearColor(0x02030a, 0)
    renderer.outputColorSpace = THREE.SRGBColorSpace
    renderer.toneMapping = THREE.ACESFilmicToneMapping
    renderer.toneMappingExposure = 1.18

    const scene = new THREE.Scene()
    scene.fog = new THREE.FogExp2(0x020714, 0.026)

    const camera = new THREE.PerspectiveCamera(48, 1, 0.1, 140)
    camera.position.set(0.8, 2.5, 12.5)

    const root = new THREE.Group()
    root.rotation.x = -0.17
    scene.add(root)

    const disposeList: Array<{ dispose: () => void }> = []
    const routeLines: THREE.Line[] = []
    const flowParticles: Array<{
      curve: THREE.CatmullRomCurve3
      mesh: THREE.Mesh
      offset: number
      speed: number
    }> = []
    const providerNodes: Array<{
      group: THREE.Group
      baseAngle: number
      radius: number
      height: number
      speed: number
    }> = []

    const cyan = new THREE.Color('#22d3ee')
    const mint = new THREE.Color('#34d399')
    const violet = new THREE.Color('#8b5cf6')
    const amber = new THREE.Color('#fbbf24')
    const white = new THREE.Color('#ffffff')

    const starCount = 5600
    const starPositions = new Float32Array(starCount * 3)
    const starColors = new Float32Array(starCount * 3)
    for (let i = 0; i < starCount; i++) {
      const i3 = i * 3
      const radius = 10 + Math.random() * 48
      const theta = Math.random() * Math.PI * 2
      const phi = Math.acos(THREE.MathUtils.randFloatSpread(2))
      starPositions[i3] = Math.sin(phi) * Math.cos(theta) * radius
      starPositions[i3 + 1] = Math.cos(phi) * radius * 0.72
      starPositions[i3 + 2] = Math.sin(phi) * Math.sin(theta) * radius - 16

      const color = white
        .clone()
        .lerp(Math.random() > 0.5 ? cyan : violet, Math.random() * 0.5)
      starColors[i3] = color.r
      starColors[i3 + 1] = color.g
      starColors[i3 + 2] = color.b
    }

    const starGeometry = new THREE.BufferGeometry()
    starGeometry.setAttribute(
      'position',
      new THREE.BufferAttribute(starPositions, 3)
    )
    starGeometry.setAttribute('color', new THREE.BufferAttribute(starColors, 3))
    disposeList.push(starGeometry)

    const starMaterial = new THREE.PointsMaterial({
      blending: THREE.AdditiveBlending,
      depthWrite: false,
      opacity: 0.82,
      size: 0.026,
      sizeAttenuation: true,
      transparent: true,
      vertexColors: true,
    })
    disposeList.push(starMaterial)
    const starfield = new THREE.Points(starGeometry, starMaterial)
    scene.add(starfield)

    const floorGeometry = new THREE.PlaneGeometry(32, 32, 32, 32)
    const floorMaterial = new THREE.MeshBasicMaterial({
      color: '#1e3a8a',
      opacity: 0.12,
      transparent: true,
      wireframe: true,
    })
    disposeList.push(floorGeometry, floorMaterial)
    const floor = new THREE.Mesh(floorGeometry, floorMaterial)
    floor.position.y = -3.4
    floor.rotation.x = -Math.PI / 2
    root.add(floor)

    const coreGroup = new THREE.Group()
    coreGroup.position.set(1.5, 0.15, 0)
    root.add(coreGroup)

    const coreGeometry = new THREE.IcosahedronGeometry(1.25, 4)
    const coreMaterial = new THREE.MeshPhysicalMaterial({
      color: '#08111f',
      emissive: '#0ea5e9',
      emissiveIntensity: 1.4,
      metalness: 0.82,
      opacity: 0.86,
      roughness: 0.16,
      transmission: 0.2,
      transparent: true,
    })
    disposeList.push(coreGeometry, coreMaterial)
    const core = new THREE.Mesh(coreGeometry, coreMaterial)
    coreGroup.add(core)

    const wireGeometry = new THREE.IcosahedronGeometry(1.42, 2)
    const wireMaterial = new THREE.MeshBasicMaterial({
      blending: THREE.AdditiveBlending,
      color: '#67e8f9',
      opacity: 0.24,
      transparent: true,
      wireframe: true,
    })
    disposeList.push(wireGeometry, wireMaterial)
    const wire = new THREE.Mesh(wireGeometry, wireMaterial)
    coreGroup.add(wire)

    const ringMaterial = new THREE.MeshBasicMaterial({
      blending: THREE.AdditiveBlending,
      color: '#22d3ee',
      opacity: 0.48,
      transparent: true,
    })
    disposeList.push(ringMaterial)
    const ringGeometry = new THREE.TorusGeometry(2.05, 0.012, 8, 180)
    disposeList.push(ringGeometry)
    for (let i = 0; i < 3; i++) {
      const ring = new THREE.Mesh(ringGeometry, ringMaterial)
      ring.rotation.set(
        i === 0 ? Math.PI / 2 : 0.25,
        i === 1 ? Math.PI / 2 : 0.18,
        i === 2 ? Math.PI / 2 : 0
      )
      coreGroup.add(ring)
    }

    const glowTexture = createGlowTexture()
    if (glowTexture) disposeList.push(glowTexture)
    const spriteMaterial = new THREE.SpriteMaterial({
      blending: THREE.AdditiveBlending,
      color: '#38bdf8',
      depthWrite: false,
      map: glowTexture ?? undefined,
      opacity: 0.72,
      transparent: true,
    })
    disposeList.push(spriteMaterial)
    const coreGlow = new THREE.Sprite(spriteMaterial)
    coreGlow.scale.set(6.2, 6.2, 1)
    coreGroup.add(coreGlow)

    const nodeGeometry = new THREE.IcosahedronGeometry(0.24, 2)
    const nodeHaloGeometry = new THREE.TorusGeometry(0.39, 0.008, 6, 48)
    const nodeMaterials = [cyan, mint, violet, amber].map(
      (color) =>
        new THREE.MeshStandardMaterial({
          color,
          emissive: color,
          emissiveIntensity: 1.9,
          metalness: 0.55,
          roughness: 0.22,
        })
    )
    const nodeHaloMaterial = new THREE.MeshBasicMaterial({
      blending: THREE.AdditiveBlending,
      color: '#7dd3fc',
      opacity: 0.38,
      transparent: true,
    })
    disposeList.push(nodeGeometry, nodeHaloGeometry, nodeHaloMaterial)
    nodeMaterials.forEach((material) => disposeList.push(material))

    const createRoute = (target: THREE.Vector3, color: THREE.Color) => {
      const start = new THREE.Vector3(1.5, 0.15, 0)
      const mid = start
        .clone()
        .lerp(target, 0.48)
        .add(new THREE.Vector3(0, THREE.MathUtils.randFloat(-0.9, 1.1), 0))
      const curve = new THREE.CatmullRomCurve3([start, mid, target])
      const points = curve.getPoints(96)
      const routeGeometry = new THREE.BufferGeometry().setFromPoints(points)
      const routeMaterial = new THREE.LineBasicMaterial({
        blending: THREE.AdditiveBlending,
        color,
        opacity: 0.2,
        transparent: true,
      })
      disposeList.push(routeGeometry, routeMaterial)
      const route = new THREE.Line(routeGeometry, routeMaterial)
      routeLines.push(route)
      root.add(route)

      const particleGeometry = new THREE.SphereGeometry(0.045, 12, 12)
      const particleMaterial = new THREE.MeshBasicMaterial({
        blending: THREE.AdditiveBlending,
        color,
        transparent: true,
      })
      disposeList.push(particleGeometry, particleMaterial)
      for (let i = 0; i < 3; i++) {
        const mesh = new THREE.Mesh(particleGeometry, particleMaterial)
        flowParticles.push({
          curve,
          mesh,
          offset: Math.random(),
          speed: THREE.MathUtils.randFloat(0.09, 0.2),
        })
        root.add(mesh)
      }
    }

    const nodeCount = 22
    for (let i = 0; i < nodeCount; i++) {
      const group = new THREE.Group()
      const radius = i % 2 === 0 ? 5.2 : 6.8
      const baseAngle = (i / nodeCount) * Math.PI * 2
      const height = Math.sin(i * 1.7) * 1.65
      const speed = THREE.MathUtils.randFloat(0.04, 0.09)
      const color = nodeMaterials[i % nodeMaterials.length]

      const node = new THREE.Mesh(nodeGeometry, color)
      const halo = new THREE.Mesh(nodeHaloGeometry, nodeHaloMaterial)
      halo.rotation.x = Math.PI / 2
      group.add(node, halo)

      group.position.set(
        1.5 + Math.cos(baseAngle) * radius,
        height,
        Math.sin(baseAngle) * radius
      )
      root.add(group)
      providerNodes.push({ group, baseAngle, radius, height, speed })

      if (i < 16) createRoute(group.position.clone(), color.color)
    }

    const cometGeometry = new THREE.BufferGeometry()
    const cometCount = 1200
    const cometPositions = new Float32Array(cometCount * 3)
    const cometColors = new Float32Array(cometCount * 3)
    for (let i = 0; i < cometCount; i++) {
      const i3 = i * 3
      const t = i / cometCount
      const angle = t * Math.PI * 10
      const radius = 8.4 * (1 - t) + Math.random() * 0.25
      cometPositions[i3] = 1.5 + Math.cos(angle) * radius
      cometPositions[i3 + 1] = THREE.MathUtils.randFloatSpread(0.24) + t * 0.4
      cometPositions[i3 + 2] = Math.sin(angle) * radius
      const color = cyan.clone().lerp(mint, t)
      cometColors[i3] = color.r
      cometColors[i3 + 1] = color.g
      cometColors[i3 + 2] = color.b
    }
    cometGeometry.setAttribute(
      'position',
      new THREE.BufferAttribute(cometPositions, 3)
    )
    cometGeometry.setAttribute(
      'color',
      new THREE.BufferAttribute(cometColors, 3)
    )
    const cometMaterial = new THREE.PointsMaterial({
      blending: THREE.AdditiveBlending,
      depthWrite: false,
      opacity: 0.54,
      size: 0.035,
      sizeAttenuation: true,
      transparent: true,
      vertexColors: true,
    })
    disposeList.push(cometGeometry, cometMaterial)
    const cometStream = new THREE.Points(cometGeometry, cometMaterial)
    root.add(cometStream)

    scene.add(new THREE.AmbientLight(0x8edfff, 0.34))
    const keyLight = new THREE.PointLight(0x22d3ee, 95, 42)
    keyLight.position.set(-2.5, 3.8, 7)
    scene.add(keyLight)
    const rimLight = new THREE.PointLight(0x8b5cf6, 75, 42)
    rimLight.position.set(7, -1, 4)
    scene.add(rimLight)

    const pointer = new THREE.Vector2(0, 0)
    const targetPointer = new THREE.Vector2(0, 0)
    const onPointerMove = (event: PointerEvent) => {
      targetPointer.x = (event.clientX / window.innerWidth - 0.5) * 2
      targetPointer.y = (event.clientY / window.innerHeight - 0.5) * 2
    }
    window.addEventListener('pointermove', onPointerMove)

    const resize = () => {
      const { clientWidth, clientHeight } = canvas
      renderer.setSize(clientWidth, clientHeight, false)
      camera.aspect = clientWidth / Math.max(clientHeight, 1)
      camera.updateProjectionMatrix()
    }
    resize()
    window.addEventListener('resize', resize)

    let frameId = 0
    const startedAt = performance.now()
    const animate = () => {
      const elapsed = (performance.now() - startedAt) / 1000
      pointer.lerp(targetPointer, 0.045)

      starfield.rotation.y = elapsed * 0.012
      root.rotation.y = elapsed * 0.038 + pointer.x * 0.11
      root.rotation.x = -0.17 + pointer.y * 0.045
      core.rotation.x = elapsed * 0.42
      core.rotation.y = elapsed * 0.68
      wire.rotation.y = -elapsed * 0.34
      coreGlow.material.opacity = 0.58 + Math.sin(elapsed * 2.4) * 0.12
      cometStream.rotation.y = -elapsed * 0.12

      providerNodes.forEach((node, index) => {
        const angle = node.baseAngle + elapsed * node.speed
        node.group.position.set(
          1.5 + Math.cos(angle) * node.radius,
          node.height + Math.sin(elapsed * 0.8 + index) * 0.16,
          Math.sin(angle) * node.radius
        )
        node.group.rotation.y = elapsed * 0.8
        node.group.rotation.x = elapsed * 0.35
      })

      routeLines.forEach((line, index) => {
        const material = line.material as THREE.LineBasicMaterial
        material.opacity = 0.12 + Math.sin(elapsed * 1.6 + index) * 0.055
      })

      flowParticles.forEach((particle) => {
        const t = (elapsed * particle.speed + particle.offset) % 1
        const point = particle.curve.getPointAt(t)
        particle.mesh.position.copy(point)
        const scale = 0.6 + Math.sin(t * Math.PI) * 1.25
        particle.mesh.scale.setScalar(scale)
      })

      camera.position.x = 0.8 + pointer.x * 0.9
      camera.position.y = 2.5 - pointer.y * 0.38
      camera.lookAt(1.1, 0, 0)
      renderer.render(scene, camera)
      frameId = window.requestAnimationFrame(animate)
    }
    animate()

    return () => {
      window.cancelAnimationFrame(frameId)
      window.removeEventListener('resize', resize)
      window.removeEventListener('pointermove', onPointerMove)
      disposeList.forEach((item) => item.dispose())
      renderer.dispose()
    }
  }, [])

  return <canvas ref={canvasRef} className='galaxy-canvas' aria-hidden='true' />
}

function GalaxyHome() {
  const { auth } = useAuthStore()
  const isAuthenticated = !!auth.user
  const ctaTarget = isAuthenticated ? '/dashboard' : '/sign-up'
  const ctaLabel = isAuthenticated ? 'Open console' : 'Enter the nexus'

  return (
    <main className='galaxy-home'>
      <NexusCanvas />
      <div className='galaxy-vignette' />

      <nav className='galaxy-nav' aria-label='Main navigation'>
        <Link to='/' className='galaxy-brand'>
          <span className='galaxy-brand-mark'>
            <Hexagon className='size-5' />
          </span>
          <span>
            <strong>DLAPI</strong>
            <small>AI model exchange</small>
          </span>
        </Link>

        <div className='galaxy-nav-links'>
          <Link to='/pricing'>Models</Link>
          <Link to='/pricing'>Pricing</Link>
          <a href='https://docs.newapi.pro' target='_blank' rel='noreferrer'>
            Docs
          </a>
          <Link to='/dashboard'>Console</Link>
          <a
            href='https://github.com/QuantumNous/new-api'
            target='_blank'
            rel='noreferrer'
            aria-label='GitHub'
          >
            <Braces className='size-4' />
          </a>
        </div>
      </nav>

      <section className='galaxy-hero'>
        <div className='galaxy-copy'>
          <div className='galaxy-kicker'>
            <Sparkles className='size-4' />
            Realtime AI API transit layer
          </div>
          <h1>
            AI Model
            <span>Nexus</span>
          </h1>
          <p>
            A programmable exchange for every model you route. One API, 40+
            providers, live policy control, quota governance, spend telemetry,
            and failover logic in the same command surface.
          </p>

          <div className='galaxy-actions'>
            <Link to={ctaTarget} className='galaxy-primary-button'>
              {ctaLabel}
              <ArrowRight className='size-4' />
            </Link>
            <a
              href='https://docs.newapi.pro'
              target='_blank'
              rel='noreferrer'
              className='galaxy-secondary-button'
            >
              Read the protocol
              <BookOpen className='size-4' />
            </a>
          </div>

          <div className='galaxy-proof'>
            {metrics.map((item) => (
              <div key={item.label}>
                <CircleDot className='size-4' />
                <strong>{item.value}</strong>
                <span>{item.label}</span>
              </div>
            ))}
          </div>
        </div>

        <aside className='galaxy-control-panel' aria-label='Live routing state'>
          <div className='galaxy-panel-header'>
            <span>
              <RadioTower className='size-4' />
              Exchange pulse
            </span>
            <em>Live</em>
          </div>

          <div className='galaxy-provider-grid'>
            {providers.slice(0, 6).map((provider, index) => (
              <div key={provider} className='galaxy-provider-node'>
                <i style={{ animationDelay: `${index * 120}ms` }} />
                <strong>{provider}</strong>
                <span>{index === 0 ? 'Primary' : 'Synced'}</span>
              </div>
            ))}
          </div>

          <div className='galaxy-log'>
            <div>
              <span>route.policy</span>
              <strong>quality-first</strong>
              <em>active</em>
            </div>
            <div>
              <span>fallback.mesh</span>
              <strong>claude → gemini</strong>
              <em>armed</em>
            </div>
            <div>
              <span>cost.guard</span>
              <strong>$0.042 / 1K tok</strong>
              <em>stable</em>
            </div>
          </div>
        </aside>
      </section>

      <section className='galaxy-capabilities' aria-label='Core capabilities'>
        {layers.map((item) => {
          const Icon = item.icon
          return (
            <div key={item.title} className='galaxy-capability'>
              <Icon className='size-5' />
              <div>
                <strong>{item.title}</strong>
                <span>{item.detail}</span>
              </div>
            </div>
          )
        })}
      </section>
    </main>
  )
}

function LoadingHome() {
  const { t } = useTranslation()

  return (
    <PublicLayout showHeader={false} showMainContainer={false}>
      <main className='galaxy-home grid min-h-svh place-items-center'>
        <div className='galaxy-kicker'>
          <Sparkles className='size-4' />
          {t('Loading...')}
        </div>
      </main>
    </PublicLayout>
  )
}

export function Home() {
  const { t } = useTranslation()
  const { content, isLoaded, isUrl } = useHomePageContent()

  if (!isLoaded) {
    return <LoadingHome />
  }

  if (content) {
    return (
      <PublicLayout showMainContainer={false}>
        <main className='overflow-x-hidden'>
          {isUrl ? (
            <iframe
              src={content}
              className='h-screen w-full border-none'
              title={t('Custom Home Page')}
            />
          ) : (
            <div className='container mx-auto py-8'>
              <Markdown className='custom-home-content'>{content}</Markdown>
            </div>
          )}
        </main>
      </PublicLayout>
    )
  }

  return (
    <PublicLayout showHeader={false} showMainContainer={false}>
      <GalaxyHome />
    </PublicLayout>
  )
}
